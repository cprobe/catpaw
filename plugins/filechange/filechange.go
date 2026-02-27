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

type ChangeCheck struct {
	Severity  string `toml:"severity"`
	TitleRule string `toml:"title_rule"`
}

type Instance struct {
	config.InternalConfig

	TimeSpan  config.Duration `toml:"time_span"`
	Filepaths []string        `toml:"filepaths"`
	Change    ChangeCheck     `toml:"change"`
}

type FileChangePlugin struct {
	config.InternalConfig
	Instances []*Instance `toml:"instances"`
}

func (p *FileChangePlugin) GetInstances() []plugins.Instance {
	ret := make([]plugins.Instance, len(p.Instances))
	for i := 0; i < len(p.Instances); i++ {
		ret[i] = p.Instances[i]
	}
	return ret
}

func init() {
	plugins.Add(pluginName, func() plugins.Plugin {
		return &FileChangePlugin{}
	})
}

func (ins *Instance) Init() error {
	if len(ins.Filepaths) == 0 {
		return fmt.Errorf("filepaths is empty")
	}

	if ins.TimeSpan == 0 {
		ins.TimeSpan = config.Duration(3 * time.Minute)
	}

	if ins.Change.Severity == "" {
		ins.Change.Severity = types.EventStatusWarning
	}

	return nil
}

func (ins *Instance) Gather(q *safe.Queue[*types.Event]) {
	var fps []string
	for _, fp := range ins.Filepaths {
		matches, err := filepath.Glob(fp)
		if err != nil {
			logger.Logger.Errorw("glob filepath fail", "filepath", fp, "error", err)
			continue
		}

		if len(matches) == 0 {
			continue
		}

		fps = append(fps, matches...)
	}

	now := time.Now()
	files := make(map[string]time.Time)

	for _, fp := range fps {
		f, e := os.Stat(fp)
		if e != nil {
			logger.Logger.Errorw("stat filepath fail", "filepath", fp, "error", e)
			continue
		}

		mtime := f.ModTime()
		if now.Sub(mtime) < time.Duration(ins.TimeSpan) {
			files[fp] = mtime
		}
	}

	if len(files) == 0 {
		q.PushFront(ins.buildEvent("files not changed"))
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
	q.PushFront(ins.buildEvent("[MD]", title, body.String()).SetEventStatus(ins.Change.Severity))
}

func (ins *Instance) buildEvent(desc ...string) *types.Event {
	tr := ins.Change.TitleRule
	if tr == "" {
		tr = "[check] [target]"
	}

	event := types.BuildEvent(map[string]string{
		"check":  "filechange::change",
		"target": strings.Join(ins.Filepaths, ","),
	}).SetTitleRule(tr)
	if len(desc) > 0 {
		event.SetDescription(strings.Join(desc, "\n"))
	}
	return event
}

var head = `| File | MTime |
| :--| --: |
`
