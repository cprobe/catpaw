package redis_sentinel

import "github.com/cprobe/catpaw/digcore/plugins"

func init() {
	plugins.Add(pluginName, func() plugins.Plugin {
		return &RedisSentinelPlugin{}
	})
}
