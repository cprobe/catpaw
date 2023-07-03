package filechange

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"flashcat.cloud/catpaw/config"
	"flashcat.cloud/catpaw/logger"
	"flashcat.cloud/catpaw/pkg/safe"
	"flashcat.cloud/catpaw/plugins"
	"flashcat.cloud/catpaw/types"
)

const (
	pluginName string = "filechange"
)

type Instance struct {
	config.InternalConfig

	TimeSpan  time.Duration `toml:"time_span"`
	Filepaths []string      `toml:"filepaths"`
	Check     string        `toml:"check"`
}

type FileChange struct {
	config.InternalConfig
	Instances []*Instance `toml:"instances"`
}

func (p *FileChange) GetInstances() []plugins.Instance {
	ret := make([]plugins.Instance, len(p.Instances))
	for i := 0; i < len(p.Instances); i++ {
		ret[i] = p.Instances[i]
	}
	return ret
}

func init() {
	plugins.Add(pluginName, func() plugins.Plugin {
		return &FileChange{}
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

	if len(ins.Filepaths) == 0 {
		logger.Logger.Error("filepaths is empty")
		return
	}

	// get all files
	var fps []string
	for _, fp := range ins.Filepaths {
		matches, err := filepath.Glob(fp)
		if err != nil {
			logger.Logger.Errorf("glob %s error: %v", fp, err)
			continue
		}

		if len(matches) == 0 {
			continue
		}

		fps = append(fps, matches...)
	}

	// check mtime
	now := time.Now()
	files := make(map[string]time.Time)

	for _, fp := range fps {
		f, e := os.Stat(fp)
		if e != nil {
			logger.Logger.Errorf("stat %s error: %v", fp, e)
			continue
		}

		mtime := f.ModTime()
		if now.Sub(mtime) < ins.TimeSpan {
			files[fp] = mtime
		}
	}

	if len(files) == 0 {
		q.PushFront(ins.buildEvent(ins.Check, "files not changed"))
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

	title := fmt.Sprintf("files changed\n\nconfiguration.filepaths:`%s`\n", ins.Filepaths)
	q.PushFront(ins.buildEvent(ins.Check, "[MD]", title, body.String()).SetEventStatus(ins.GetDefaultSeverity()))
}

func (ins *Instance) buildEvent(check string, desc ...string) *types.Event {
	event := types.BuildEvent(map[string]string{"check": check}).SetTitleRule("$check")
	if len(desc) > 0 {
		event.SetDescription(strings.Join(desc, "\n"))
	}
	return event
}

var head = `| File | MTime |
| :--| --: |
`
