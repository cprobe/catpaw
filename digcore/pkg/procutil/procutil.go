package procutil

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"

	"github.com/shirou/gopsutil/v3/process"
)

type PID int32

// FastProcessList returns lightweight process handles for all running PIDs.
// Each handle only has the PID populated; attributes are fetched lazily on demand.
func FastProcessList() ([]*process.Process, error) {
	pids, err := process.Pids()
	if err != nil {
		return nil, err
	}

	result := make([]*process.Process, len(pids))
	for i, pid := range pids {
		result[i] = &process.Process{Pid: pid}
	}
	return result, nil
}

// CountAllProcesses returns the total number of running processes.
func CountAllProcesses() (int, error) {
	pids, err := process.Pids()
	if err != nil {
		return 0, err
	}
	return len(pids), nil
}

// ReadPidFile reads a PID from the given file and returns it as a single-element slice.
func ReadPidFile(path string) ([]PID, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read pidfile '%s': %v", path, err)
	}
	pid, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 32)
	if err != nil {
		return nil, fmt.Errorf("invalid pid in '%s': %v", path, err)
	}
	if pid <= 0 {
		return nil, fmt.Errorf("invalid pid %d in '%s': must be positive", pid, path)
	}
	return []PID{PID(pid)}, nil
}

// IsProcessGone returns true if the error indicates the process no longer exists.
func IsProcessGone(err error) bool {
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	if errors.Is(err, syscall.ESRCH) {
		return true
	}
	return false
}
