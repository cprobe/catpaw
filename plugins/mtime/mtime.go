package mtime

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"flashcat.cloud/catpaw/config"
	"flashcat.cloud/catpaw/pkg/safe"
	"flashcat.cloud/catpaw/plugins"
	"flashcat.cloud/catpaw/types"
	"github.com/toolkits/pkg/file"
)

const (
	pluginName string = "mtime"
)

type Instance struct {
	config.InternalConfig

	TimeSpan  config.Duration `toml:"time_span"`
	Directory string          `toml:"directory"`
	Check     string          `toml:"check"`
}

type MTimePlugin struct {
	config.InternalConfig
	Instances []*Instance `toml:"instances"`
}

func (p *MTimePlugin) GetInstances() []plugins.Instance {
	ret := make([]plugins.Instance, len(p.Instances))
	for i := 0; i < len(p.Instances); i++ {
		ret[i] = p.Instances[i]
	}
	return ret
}

func init() {
	plugins.Add(pluginName, func() plugins.Plugin {
		return &MTimePlugin{}
	})
}

func (ins *Instance) Init() error {
	if ins.Check == "" {
		return fmt.Errorf("check is required")
	}

	if ins.Directory == "" {
		return fmt.Errorf("directory is required")
	}

	if !file.IsExist(ins.Directory) {
		return fmt.Errorf("directory %s does not exist", ins.Directory)
	}

	if ins.TimeSpan == 0 {
		ins.TimeSpan = config.Duration(3 * time.Minute)
	}

	return nil
}

func (ins *Instance) Gather(q *safe.Queue[*types.Event]) {
	now := time.Now()
	files := make(map[string]time.Time)

	if err := filepath.WalkDir(ins.Directory, func(path string, di fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if di.IsDir() {
			return nil
		}

		fileinfo, err := di.Info()
		if err != nil {
			return err
		}

		mtime := fileinfo.ModTime()
		if now.Sub(mtime) < time.Duration(ins.TimeSpan) {
			files[path] = mtime
		}

		return nil
	}); err != nil {
		q.PushFront(ins.buildEvent(ins.Directory, ins.Check, fmt.Sprintf("walk directory %s error: %v", ins.Directory, err)).SetEventStatus(ins.GetDefaultSeverity()))
		return
	}

	if len(files) == 0 {
		q.PushFront(ins.buildEvent(ins.Directory, ins.Check, fmt.Sprintf("files not changed or created in directory %s", ins.Directory)))
		return
	}

	var body strings.Builder
	body.WriteString(head)

	for fp, mtime := range files {
		body.WriteString("| ")
		body.WriteString(fp)
		body.WriteString(" | ")
		body.WriteString(mtime.Format("2006-01-02 15:04:05"))
		body.WriteString(" |\n")
	}

	q.PushFront(ins.buildEvent(ins.Directory, ins.Check, body.String()).SetEventStatus(ins.GetDefaultSeverity()))
}

func (ins *Instance) buildEvent(dir, check string, desc ...string) *types.Event {
	event := types.BuildEvent(map[string]string{"directory": dir, "check": check}).SetTitleRule("$check")
	if len(desc) > 0 {
		event.SetDescription(desc[0])
	}
	return event
}

var head = `[MD]| File | MTime |
| :--| --: |
`
