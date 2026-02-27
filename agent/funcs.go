package agent

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/cprobe/catpaw/config"
	"github.com/toolkits/pkg/file"
)

func loadFileConfigs() (map[string]*PluginConfig, error) {
	dirs, err := file.DirsUnder(config.Config.ConfigDir)
	if err != nil {
		return nil, fmt.Errorf("failed to get config dirs: %v", err)
	}

	ret := make(map[string]*PluginConfig)

	for _, dir := range dirs {
		if !strings.HasPrefix(dir, "p.") {
			continue
		}

		name := dir[len("p."):]

		mtime, content, err := readPluginDir(name)
		if err != nil {
			return nil, err
		}

		if mtime == -1 || len(content) == 0 {
			continue
		}

		ret[name] = &PluginConfig{
			Digest:      fmt.Sprint(mtime),
			FileContent: content,
			Source:      "file",
		}
	}

	return ret, nil
}

// readPluginDir reads all .toml files under conf.d/p.{name}/, returning
// the max mtime and concatenated content. mtime == -1 means no .toml files found.
func readPluginDir(name string) (int64, []byte, error) {
	pluginDir := path.Join(config.Config.ConfigDir, "p."+name)

	files, err := file.FilesUnder(pluginDir)
	if err != nil {
		return 0, nil, fmt.Errorf("failed to list files under %s: %v", pluginDir, err)
	}

	sort.Strings(files)

	var maxmt int64 = -1
	var content []byte
	var tomlCount int

	for _, f := range files {
		if !strings.HasSuffix(f, ".toml") {
			continue
		}

		fp := path.Join(pluginDir, f)
		mtime, err := file.FileMTime(fp)
		if err != nil {
			return 0, nil, fmt.Errorf("failed to get mtime of %s: %v", fp, err)
		}

		if mtime > maxmt {
			maxmt = mtime
		}

		if tomlCount > 0 {
			content = append(content, '\n', '\n')
		}

		bs, err := file.ReadBytes(fp)
		if err != nil {
			return 0, nil, fmt.Errorf("failed to read %s: %v", fp, err)
		}

		content = append(content, bs...)
		tomlCount++
	}

	return maxmt, content, nil
}

func parseFilter(filterStr string) map[string]struct{} {
	filters := strings.Split(filterStr, ":")
	filtermap := make(map[string]struct{})
	for i := 0; i < len(filters); i++ {
		if strings.TrimSpace(filters[i]) == "" {
			continue
		}
		filtermap[strings.TrimSpace(filters[i])] = struct{}{}
	}
	return filtermap
}
