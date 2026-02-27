//go:build windows
// +build windows

package procnum

import (
	"github.com/shirou/gopsutil/v3/process"
)

// processExecName returns the process name (e.g. "nginx.exe"),
// providing consistent cross-platform semantics with the non-Windows variant.
func processExecName(p *process.Process) (string, error) {
	return p.Name()
}
