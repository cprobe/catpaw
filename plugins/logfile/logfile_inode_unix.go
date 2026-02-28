//go:build !windows

package logfile

import (
	"os"
	"syscall"
)

func getInode(info os.FileInfo) uint64 {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return stat.Ino
	}
	return 0
}
