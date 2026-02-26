package agent

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"flashcat.cloud/catpaw/config"
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

		// use this as map key
		name := dir[len("p."):]

		pluginDir := path.Join(config.Config.ConfigDir, dir)
		files, err := file.FilesUnder(pluginDir)
		if err != nil {
			return nil, fmt.Errorf("failed to list files under %s: %v", pluginDir, err)
		}

		if len(files) == 0 {
			continue
		}

		sort.Strings(files)

		var maxmt int64
		var bytes []byte
		for i := 0; i < len(files); i++ {
			if !strings.HasSuffix(files[i], ".toml") {
				continue
			}

			filepath := path.Join(pluginDir, files[i])
			mtime, err := file.FileMTime(filepath)
			if err != nil {
				return nil, fmt.Errorf("failed to get mtime of %s: %v", filepath, err)
			}

			if mtime > maxmt {
				maxmt = mtime
			}

			if i > 0 {
				bytes = append(bytes, '\n')
				bytes = append(bytes, '\n')
			}

			bs, err := file.ReadBytes(filepath)
			if err != nil {
				return nil, fmt.Errorf("failed to read %s: %v", filepath, err)
			}

			bytes = append(bytes, bs...)
		}

		ret[name] = &PluginConfig{
			Digest:      fmt.Sprint(maxmt),
			FileContent: bytes,
			Source:      "file",
		}
	}

	return ret, nil
}

// return -1 means no configuration files under plugin directory
func getMTime(name string) (int64, error) {
	pluginDir := path.Join(config.Config.ConfigDir, "p."+name)

	files, err := file.FilesUnder(pluginDir)
	if err != nil {
		return 0, fmt.Errorf("failed to list files under %s: %v", pluginDir, err)
	}

	var maxmt int64 = -1
	for i := 0; i < len(files); i++ {
		if !strings.HasSuffix(files[i], ".toml") {
			continue
		}

		filepath := path.Join(pluginDir, files[i])
		mtime, err := file.FileMTime(filepath)
		if err != nil {
			return 0, fmt.Errorf("failed to get mtime of %s: %v", filepath, err)
		}

		if mtime > maxmt {
			maxmt = mtime
		}
	}

	return maxmt, nil
}

// get plugin configuration file content
func getFileContent(name string) ([]byte, error) {
	pluginDir := path.Join(config.Config.ConfigDir, "p."+name)

	files, err := file.FilesUnder(pluginDir)
	if err != nil {
		return nil, fmt.Errorf("failed to list files under %s: %v", pluginDir, err)
	}

	if len(files) == 0 {
		return nil, nil
	}

	sort.Strings(files)

	var bytes []byte
	for i := 0; i < len(files); i++ {
		if !strings.HasSuffix(files[i], ".toml") {
			continue
		}

		filepath := path.Join(pluginDir, files[i])

		if i > 0 {
			bytes = append(bytes, '\n')
			bytes = append(bytes, '\n')
		}

		bs, err := file.ReadBytes(filepath)
		if err != nil {
			return nil, fmt.Errorf("failed to read %s: %v", filepath, err)
		}

		bytes = append(bytes, bs...)
	}

	return bytes, nil
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
