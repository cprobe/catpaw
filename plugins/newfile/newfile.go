package newfile

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"time"

	"flashcat.cloud/catpaw/config"
	"flashcat.cloud/catpaw/logger"
	"flashcat.cloud/catpaw/pkg/safe"
	"flashcat.cloud/catpaw/plugins"
	"flashcat.cloud/catpaw/types"
	"github.com/toolkits/pkg/file"
)

const (
	pluginName string = "newfile"
)

type Instance struct {
	config.InternalConfig

	MTimeSpan time.Duration `toml:"mtime_span"`
	CTimeSpan string        `toml:"ctime_span"`
	Directory string        `toml:"directory"`
	Check     string        `toml:"check"`
}

type NewFile struct {
	config.InternalConfig
	Instances []*Instance `toml:"instances"`
}

func (p *NewFile) IsSystemPlugin() bool {
	return false
}

func (p *NewFile) GetInstances() []plugins.Instance {
	ret := make([]plugins.Instance, len(p.Instances))
	for i := 0; i < len(p.Instances); i++ {
		ret[i] = p.Instances[i]
	}
	return ret
}

func init() {
	plugins.Add(pluginName, func() plugins.Plugin {
		return &NewFile{}
	})
}

func (ins *Instance) Gather(q *safe.Queue[*types.Event]) {
	if ins.MTimeSpan == 0 && ins.CTimeSpan == "" {
		logger.Logger.Error("ctime_span and mtime_span is empty")
		return
	}

	if ins.Check == "" {
		logger.Logger.Error("check is empty")
		return
	}

	event := types.BuildEvent(map[string]string{"directory": ins.Directory, "check": ins.Check}).SetTitleRule("$check")
	if !file.IsExist(ins.Directory) {
		q.PushFront(event.SetEventStatus(ins.GetDefaultSeverity()).SetDescription(fmt.Sprintf("directory %s not exist", ins.Directory)))
		return
	}

	now := time.Now()

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
		if now.Sub(mtime) < ins.MTimeSpan {
			fmt.Println("path:", path, "mtime:", mtime, "----", now.Sub(mtime))
		}

		return nil
	}); err != nil {
		q.PushFront(event.SetEventStatus(ins.GetDefaultSeverity()).SetDescription(fmt.Sprintf("walk directory %s error: %v", ins.Directory, err)))
		return
	}

	q.PushFront(event.SetDescription(fmt.Sprintf("files not changed or created in directory %s", ins.Directory)))
}
