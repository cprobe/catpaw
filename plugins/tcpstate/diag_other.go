//go:build !linux

package tcpstate

import "fmt"

func init() {
	queryStatesFn = func() (*stateCounts, error) {
		return nil, fmt.Errorf("netlink not available on %s", runtimeGOOS)
	}
	readTimeWaitFn = func() (uint64, error) {
		return 0, fmt.Errorf("/proc/net/sockstat not available on %s", runtimeGOOS)
	}
}
