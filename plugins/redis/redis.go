// Package redis provides a catpaw remote plugin for monitoring Redis instances
// across standalone, master/replica, and Redis Cluster deployments. It is the
// reference implementation for remote plugins that need accessor-based
// collection, partial config reuse, multi-target gather, AI diagnosis tools,
// and conservative default checks.
package redis

import "github.com/cprobe/catpaw/digcore/plugins"

func init() {
	plugins.Add(pluginName, func() plugins.Plugin {
		return &RedisPlugin{}
	})
}
