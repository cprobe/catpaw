package cfg

import (
	"bytes"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/koding/multiconfig"
	"github.com/toolkits/pkg/file"
)

type ConfigFormat string

const (
	YamlFormat ConfigFormat = "yaml"
	TomlFormat ConfigFormat = "toml"
	JsonFormat ConfigFormat = "json"
)

const (
	DefaultConfigFile       = "config.toml"
	LocalOverrideConfigFile = "config.local.toml"
)

type ConfigWithFormat struct {
	Config   string       `json:"config"`
	Format   ConfigFormat `json:"format"`
	checkSum string       `json:"-"`
}

func (cwf *ConfigWithFormat) CheckSum() string {
	return cwf.checkSum
}

func (cwf *ConfigWithFormat) SetCheckSum(checkSum string) {
	cwf.checkSum = checkSum
}

func GuessFormat(fpath string) ConfigFormat {
	if strings.HasSuffix(fpath, ".json") {
		return JsonFormat
	}
	if strings.HasSuffix(fpath, ".yaml") || strings.HasSuffix(fpath, ".yml") {
		return YamlFormat
	}
	return TomlFormat
}

func LoadConfigByDir(configDir string, configPtr interface{}) error {
	loaders := []multiconfig.Loader{
		&multiconfig.TagLoader{},
		&multiconfig.EnvironmentLoader{},
	}

	files, err := file.FilesUnder(configDir)
	if err != nil {
		return fmt.Errorf("failed to list files under: %s : %v", configDir, err)
	}
	files = orderedConfigFiles(files)

	for _, fpath := range files {
		fullPath := path.Join(configDir, fpath)

		switch {
		case strings.HasSuffix(fpath, ".toml"):
			loaders = append(loaders, &multiconfig.TOMLLoader{Path: fullPath})
		case strings.HasSuffix(fpath, ".json"):
			loaders = append(loaders, &multiconfig.JSONLoader{Path: fullPath})
		case strings.HasSuffix(fpath, ".yaml") || strings.HasSuffix(fpath, ".yml"):
			loaders = append(loaders, &multiconfig.YAMLLoader{Path: fullPath})
		}
	}

	m := multiconfig.DefaultLoader{
		Loader:    multiconfig.MultiLoader(loaders...),
		Validator: multiconfig.MultiValidator(&multiconfig.RequiredValidator{}),
	}
	return m.Load(configPtr)
}

func orderedConfigFiles(files []string) []string {
	sort.Strings(files)

	ordered := make([]string, 0, len(files))

	appendIfPresent := func(name string) {
		for _, file := range files {
			if file == name {
				ordered = append(ordered, file)
				return
			}
		}
	}

	appendIfPresent(DefaultConfigFile)

	for _, file := range files {
		if file == DefaultConfigFile || file == LocalOverrideConfigFile {
			continue
		}
		ordered = append(ordered, file)
	}

	appendIfPresent(LocalOverrideConfigFile)

	return ordered
}

func LoadConfigs(configs []ConfigWithFormat, configPtr interface{}) error {
	var (
		tBuf, yBuf, jBuf []byte
	)
	loaders := []multiconfig.Loader{
		&multiconfig.TagLoader{},
		&multiconfig.EnvironmentLoader{},
	}
	for _, c := range configs {
		switch c.Format {
		case TomlFormat:
			tBuf = append(tBuf, []byte("\n\n")...)
			tBuf = append(tBuf, []byte(c.Config)...)
		case YamlFormat:
			yBuf = append(yBuf, []byte(c.Config)...)
		case JsonFormat:
			jBuf = append(jBuf, []byte(c.Config)...)
		}
	}

	if len(tBuf) != 0 {
		loaders = append(loaders, &multiconfig.TOMLLoader{Reader: bytes.NewReader(tBuf)})
	}
	if len(yBuf) != 0 {
		loaders = append(loaders, &multiconfig.YAMLLoader{Reader: bytes.NewReader(yBuf)})
	}
	if len(jBuf) != 0 {
		loaders = append(loaders, &multiconfig.JSONLoader{Reader: bytes.NewReader(jBuf)})
	}

	m := multiconfig.DefaultLoader{
		Loader:    multiconfig.MultiLoader(loaders...),
		Validator: multiconfig.MultiValidator(&multiconfig.RequiredValidator{}),
	}
	return m.Load(configPtr)
}

func LoadSingleConfig(c ConfigWithFormat, configPtr interface{}) error {
	loaders := []multiconfig.Loader{
		&multiconfig.TagLoader{},
		&multiconfig.EnvironmentLoader{},
	}

	switch c.Format {
	case TomlFormat:
		loaders = append(loaders, &multiconfig.TOMLLoader{Reader: bytes.NewReader([]byte(c.Config))})
	case YamlFormat:
		loaders = append(loaders, &multiconfig.YAMLLoader{Reader: bytes.NewReader([]byte(c.Config))})
	case JsonFormat:
		loaders = append(loaders, &multiconfig.JSONLoader{Reader: bytes.NewReader([]byte(c.Config))})

	}

	m := multiconfig.DefaultLoader{
		Loader:    multiconfig.MultiLoader(loaders...),
		Validator: multiconfig.MultiValidator(&multiconfig.RequiredValidator{}),
	}
	return m.Load(configPtr)
}
