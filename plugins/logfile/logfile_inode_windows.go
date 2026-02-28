//go:build windows

package logfile

import "os"

func getInode(_ os.FileInfo) uint64 {
	return 0
}
