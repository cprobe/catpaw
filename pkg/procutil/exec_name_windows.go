//go:build windows
// +build windows

package procutil

import (
	"github.com/shirou/gopsutil/v3/process"
)

// ProcessExecName returns the process name (e.g. "nginx.exe"),
// providing consistent cross-platform semantics with the non-Windows variant.
func ProcessExecName(p *process.Process) (string, error) {
	return p.Name()
}
