package http

import (
	"flashcat.cloud/catpaw/config"
	"flashcat.cloud/catpaw/plugins"
)

type Instance struct {
	config.InternalConfig
}

type Http struct {
	config.InternalConfig
	Instances []*Instance `toml:"instances"`
}

func init() {
	plugins.Add("http", func() plugins.Plugin {
		return &Http{}
	})
}
