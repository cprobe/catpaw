package cmdx

import (
	"bytes"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"time"

	"flashcat.cloud/catpaw/pkg/shell"
)

const MaxStderrBytes = 512

func RunTimeout(cmd *exec.Cmd, timeout time.Duration) (error, bool) {
	err := CmdStart(cmd)
	if err != nil {
		return err, false
	}

	return CmdWait(cmd, timeout)
}

func CommandRun(command string, timeout time.Duration) ([]byte, []byte, error) {
	splitCmd, err := shell.QuoteSplit(command)
	if err != nil || len(splitCmd) == 0 {
		return nil, nil, fmt.Errorf("exec: unable to parse command, %s", err)
	}

	cmd := exec.Command(splitCmd[0], splitCmd[1:]...)

	var (
		out    bytes.Buffer
		stderr bytes.Buffer
	)
	cmd.Stdout = &out
	cmd.Stderr = &stderr

	runError, runTimeout := RunTimeout(cmd, timeout)
	if runTimeout {
		return nil, nil, fmt.Errorf("exec %s timeout", command)
	}

	out = RemoveWindowsCarriageReturns(out)
	if stderr.Len() > 0 {
		stderr = RemoveWindowsCarriageReturns(stderr)
		// stderr = TruncateStderr(stderr)
	}

	return out.Bytes(), stderr.Bytes(), runError
}

func TruncateStderr(buf bytes.Buffer) bytes.Buffer {
	didTruncate := false
	if buf.Len() > MaxStderrBytes {
		buf.Truncate(MaxStderrBytes)
		didTruncate = true
	}
	if i := bytes.IndexByte(buf.Bytes(), '\n'); i > 0 {
		if i < buf.Len()-1 {
			didTruncate = true
		}
		buf.Truncate(i)
	}
	if didTruncate {
		//nolint:errcheck,revive
		buf.WriteString("...")
	}
	return buf
}

// RemoveWindowsCarriageReturns removes all carriage returns from the input if the
// OS is Windows. It does not return any errors.
func RemoveWindowsCarriageReturns(b bytes.Buffer) bytes.Buffer {
	if runtime.GOOS == "windows" {
		var buf bytes.Buffer
		for {
			byt, err := b.ReadBytes(0x0D)
			byt = bytes.TrimRight(byt, "\x0d")
			if len(byt) > 0 {
				_, _ = buf.Write(byt)
			}
			if err == io.EOF {
				return buf
			}
		}
	}
	return b
}
