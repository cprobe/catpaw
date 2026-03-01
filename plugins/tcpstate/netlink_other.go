//go:build !linux

package tcpstate

import "fmt"

func init() {
	queryStatesFn = func() (*stateCounts, error) {
		return nil, fmt.Errorf("netlink not available on %s", runtimeGOOS)
	}
}
