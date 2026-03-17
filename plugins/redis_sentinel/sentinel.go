package redis_sentinel

import "github.com/cprobe/catpaw/plugins"

func init() {
	plugins.Add(pluginName, func() plugins.Plugin {
		return &RedisSentinelPlugin{}
	})
}
