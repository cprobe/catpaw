package mtime

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"flashcat.cloud/catpaw/config"
	"flashcat.cloud/catpaw/logger"
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

	TimeSpan  time.Duration `toml:"time_span"`
	Directory string        `toml:"directory"`
	Check     string        `toml:"check"`
}

type MTime struct {
	config.InternalConfig
	Instances []*Instance `toml:"instances"`
}

func (p *MTime) IsSystemPlugin() bool {
	return false
}

func (p *MTime) GetInstances() []plugins.Instance {
	ret := make([]plugins.Instance, len(p.Instances))
	for i := 0; i < len(p.Instances); i++ {
		ret[i] = p.Instances[i]
	}
	return ret
}

func init() {
	plugins.Add(pluginName, func() plugins.Plugin {
		return &MTime{}
	})
}

func (ins *Instance) Gather(q *safe.Queue[*types.Event]) {
	if ins.TimeSpan == 0 {
		ins.TimeSpan = 3 * time.Minute
	}

	if ins.Check == "" {
		logger.Logger.Error("check is empty")
		return
	}

	if !file.IsExist(ins.Directory) {
		logger.Logger.Warnf("directory %s not exist", ins.Directory)
		return
	}

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
		if now.Sub(mtime) < ins.TimeSpan {
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
