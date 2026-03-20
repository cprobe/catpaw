//go:build !windows
// +build !windows

package procutil

import (
	"path/filepath"

	"github.com/shirou/gopsutil/v3/process"
)

// ProcessExecName returns the base name of the executable (without path),
// providing consistent cross-platform semantics with the Windows variant.
func ProcessExecName(p *process.Process) (string, error) {
	exe, err := p.Exe()
	if err != nil {
		return "", err
	}
	return filepath.Base(exe), nil
}
