package tcpstate

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

const sockstatPath = "/proc/net/sockstat"

func init() {
	readTimeWaitFn = readSockstatTimeWait
}

func readSockstatTimeWait() (uint64, error) {
	data, err := os.ReadFile(sockstatPath)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", sockstatPath, err)
	}
	return parseSockstatTimeWait(data)
}

func parseSockstatTimeWait(data []byte) (uint64, error) {
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "TCP:") {
			continue
		}
		fields := strings.Fields(line)
		for i, f := range fields {
			if f == "tw" && i+1 < len(fields) {
				v, err := strconv.ParseUint(fields[i+1], 10, 64)
				if err != nil {
					return 0, fmt.Errorf("parse tw value %q: %w", fields[i+1], err)
				}
				return v, nil
			}
		}
	}
	return 0, fmt.Errorf("tw field not found in %s", sockstatPath)
}
