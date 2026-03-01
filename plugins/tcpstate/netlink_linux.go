//go:build linux

package tcpstate

import (
	"encoding/binary"
	"errors"
	"fmt"
	"syscall"
)

const (
	netlinkSockDiag  = 4  // NETLINK_SOCK_DIAG (a.k.a. NETLINK_INET_DIAG)
	sockDiagByFamily = 20 // SOCK_DIAG_BY_FAMILY message type

	tcpEstablished = 1
	tcpTimeWait    = 6
	tcpCloseWait   = 8

	// Only query the 3 states we care about — kernel skips sockets in other states.
	statesFilter = (1 << tcpEstablished) | (1 << tcpTimeWait) | (1 << tcpCloseWait)

	inetDiagReqV2Size = 56 // 1+1+1+1+4+48
	nlMsgHdrSize      = 16

	recvBufSize = 256 * 1024
)

func init() {
	queryStatesFn = queryNetlinkTcpStates
}

func queryNetlinkTcpStates() (*stateCounts, error) {
	fd, err := syscall.Socket(
		syscall.AF_NETLINK,
		syscall.SOCK_DGRAM|syscall.SOCK_CLOEXEC,
		netlinkSockDiag,
	)
	if err != nil {
		return nil, fmt.Errorf("netlink socket: %w", err)
	}
	defer syscall.Close(fd)

	if err := syscall.Bind(fd, &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK}); err != nil {
		return nil, fmt.Errorf("netlink bind: %w", err)
	}

	tv := syscall.Timeval{Sec: 5}
	if err := syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv); err != nil {
		return nil, fmt.Errorf("set recv timeout: %w", err)
	}

	counts := &stateCounts{}
	for _, family := range []uint8{syscall.AF_INET, syscall.AF_INET6} {
		if err := queryFamily(fd, family, counts); err != nil {
			if family == syscall.AF_INET6 && isIPv6Unavailable(err) {
				continue
			}
			return nil, err
		}
	}
	return counts, nil
}

func queryFamily(fd int, family uint8, counts *stateCounts) error {
	req := buildDiagRequest(family)
	addr := &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK}

	if err := syscall.Sendto(fd, req, 0, addr); err != nil {
		return fmt.Errorf("netlink send (family=%d): %w", family, err)
	}

	buf := make([]byte, recvBufSize)
	for {
		n, _, err := syscall.Recvfrom(fd, buf, 0)
		if err != nil {
			return fmt.Errorf("netlink recv (family=%d): %w", family, err)
		}
		if n < nlMsgHdrSize {
			return fmt.Errorf("netlink recv: short read (%d bytes)", n)
		}

		msgs, err := syscall.ParseNetlinkMessage(buf[:n])
		if err != nil {
			return fmt.Errorf("parse netlink message: %w", err)
		}

		for _, m := range msgs {
			switch m.Header.Type {
			case syscall.NLMSG_DONE:
				return nil
			case syscall.NLMSG_ERROR:
				return parseNlError(m.Data)
			default:
				if len(m.Data) >= 2 {
					switch m.Data[1] {
					case tcpEstablished:
						counts.established++
					case tcpCloseWait:
						counts.closeWait++
					case tcpTimeWait:
						counts.timeWait++
					}
				}
			}
		}
	}
}

func buildDiagRequest(family uint8) []byte {
	const totalSize = nlMsgHdrSize + inetDiagReqV2Size
	msg := make([]byte, totalSize)

	ne := binary.NativeEndian

	// nlmsghdr — native byte order per Netlink spec
	ne.PutUint32(msg[0:4], totalSize)
	ne.PutUint16(msg[4:6], sockDiagByFamily)
	ne.PutUint16(msg[6:8], syscall.NLM_F_REQUEST|syscall.NLM_F_DUMP)

	// inet_diag_req_v2
	msg[16] = family
	msg[17] = syscall.IPPROTO_TCP
	ne.PutUint32(msg[20:24], statesFilter)

	return msg
}

func parseNlError(data []byte) error {
	if len(data) < 4 {
		return fmt.Errorf("netlink error: truncated payload (%d bytes)", len(data))
	}
	errno := int32(binary.NativeEndian.Uint32(data[:4]))
	if errno == 0 {
		return nil
	}
	if errno > 0 {
		errno = -errno
	}
	return syscall.Errno(-errno)
}

func isIPv6Unavailable(err error) bool {
	return errors.Is(err, syscall.ENOENT) ||
		errors.Is(err, syscall.EAFNOSUPPORT) ||
		errors.Is(err, syscall.EPROTONOSUPPORT)
}
