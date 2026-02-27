package exec

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"flashcat.cloud/catpaw/config"
	"flashcat.cloud/catpaw/logger"
	"flashcat.cloud/catpaw/pkg/cmdx"
	"flashcat.cloud/catpaw/pkg/safe"
	"flashcat.cloud/catpaw/pkg/shell"
	"flashcat.cloud/catpaw/plugins"
	"flashcat.cloud/catpaw/types"
	"github.com/toolkits/pkg/concurrent/semaphore"
)

const pluginName = "exec"

const (
	maxStdoutBytes = 1 << 20   // 1MB
	maxStderrBytes = 256 << 10 // 256KB
)

// limitedWriter wraps bytes.Buffer with a size cap to prevent OOM
// from scripts that produce unbounded output.
type limitedWriter struct {
	buf bytes.Buffer
	max int
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if remaining := w.max - w.buf.Len(); remaining > 0 {
		if len(p) > remaining {
			w.buf.Write(p[:remaining])
		} else {
			w.buf.Write(p)
		}
	}
	return len(p), nil
}

type Instance struct {
	config.InternalConfig

	Commands    []string          `toml:"commands"`
	Timeout     config.Duration   `toml:"timeout"`
	Concurrency int               `toml:"concurrency"`
	Mode        string            `toml:"mode"`
	EnvVars     map[string]string `toml:"env_vars"`
}

type ExecPlugin struct {
	config.InternalConfig
	Instances []*Instance `toml:"instances"`
}

func (p *ExecPlugin) GetInstances() []plugins.Instance {
	ret := make([]plugins.Instance, len(p.Instances))
	for i := 0; i < len(p.Instances); i++ {
		ret[i] = p.Instances[i]
	}
	return ret
}

func init() {
	plugins.Add(pluginName, func() plugins.Plugin {
		return &ExecPlugin{}
	})
}

func (ins *Instance) Init() error {
	if len(ins.Commands) == 0 {
		return nil
	}

	if ins.Timeout == 0 {
		ins.Timeout = config.Duration(10 * time.Second)
	}

	if ins.Concurrency == 0 {
		ins.Concurrency = 5
	}

	if ins.Mode == "" {
		ins.Mode = "json"
	}
	if ins.Mode != "json" && ins.Mode != "nagios" {
		return fmt.Errorf("mode must be 'json' or 'nagios', got '%s'", ins.Mode)
	}

	return nil
}

func (ins *Instance) Gather(q *safe.Queue[*types.Event]) {
	if len(ins.Commands) == 0 {
		return
	}

	var commands []string
	for _, pattern := range ins.Commands {
		cmdAndArgs := strings.SplitN(pattern, " ", 2)
		if len(cmdAndArgs) == 0 {
			continue
		}

		matches, err := filepath.Glob(cmdAndArgs[0])
		if err != nil {
			logger.Logger.Errorw("failed to get filepath glob", "error", err, "pattern", cmdAndArgs[0])
			continue
		}

		if len(matches) == 0 {
			commands = append(commands, pattern)
		} else {
			for _, match := range matches {
				if len(cmdAndArgs) == 1 {
					commands = append(commands, match)
				} else {
					commands = append(commands,
						strings.Join([]string{match, cmdAndArgs[1]}, " "))
				}
			}
		}
	}

	if len(commands) == 0 {
		logger.Logger.Warnln("no commands after parse")
		return
	}

	wg := new(sync.WaitGroup)
	se := semaphore.NewSemaphore(ins.Concurrency)
	for _, command := range commands {
		wg.Add(1)
		go func(command string) {
			se.Acquire()
			defer func() {
				se.Release()
				wg.Done()
			}()
			ins.gather(q, command)
		}(command)
	}
	wg.Wait()
}

func (ins *Instance) gather(q *safe.Queue[*types.Event], command string) {
	stdout, stderr, exitCode, err := ins.runCommand(command)

	if err != nil {
		q.PushFront(ins.buildErrorEvent(command, err, combineOutput(stdout, stderr)))
		return
	}

	if len(stderr) > 0 {
		logger.Logger.Debugw("command stderr output", "command", command, "stderr", string(stderr))
	}

	switch ins.Mode {
	case "nagios":
		ins.gatherNagios(q, command, stdout, exitCode)
	default:
		ins.gatherJSON(q, command, stdout, exitCode)
	}
}

// runCommand executes a command with timeout and environment variables,
// returning stdout, stderr, exit code, and error.
// A non-zero exit code is NOT treated as an error (returned via exitCode).
// Error is only set for actual execution failures (command not found, timeout, etc).
// On timeout, partial stdout/stderr captured before the kill is still returned.
func (ins *Instance) runCommand(command string) ([]byte, []byte, int, error) {
	splitCmd, err := shell.QuoteSplit(command)
	if err != nil || len(splitCmd) == 0 {
		return nil, nil, -1, fmt.Errorf("unable to parse command: %v", err)
	}

	cmd := exec.Command(splitCmd[0], splitCmd[1:]...)

	if len(ins.EnvVars) > 0 {
		cmd.Env = os.Environ()
		for k, v := range ins.EnvVars {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}

	outBuf := &limitedWriter{max: maxStdoutBytes}
	errBuf := &limitedWriter{max: maxStderrBytes}
	cmd.Stdout = outBuf
	cmd.Stderr = errBuf

	runErr, timedOut := cmdx.RunTimeout(cmd, time.Duration(ins.Timeout))

	outResult := cmdx.RemoveWindowsCarriageReturns(outBuf.buf)
	errResult := cmdx.RemoveWindowsCarriageReturns(errBuf.buf)

	if timedOut {
		return outResult.Bytes(), errResult.Bytes(), -1,
			fmt.Errorf("command timed out after %s", time.Duration(ins.Timeout))
	}

	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			return outResult.Bytes(), errResult.Bytes(), exitErr.ExitCode(), nil
		}
		return nil, errResult.Bytes(), -1, runErr
	}

	return outResult.Bytes(), errResult.Bytes(), 0, nil
}

// gatherNagios parses Nagios plugin output format:
//
//	exit code: 0=OK, 1=Warning, 2=Critical, other=Critical
//	stdout line 1: STATUS TEXT | perfdata
//	stdout line 2+: long text (optional)
func (ins *Instance) gatherNagios(q *safe.Queue[*types.Event], command string, stdout []byte, exitCode int) {
	status := nagiosExitCodeToStatus(exitCode)

	output := strings.TrimSpace(string(stdout))
	var statusLine, perfData, longText string
	if output != "" {
		lines := strings.SplitN(output, "\n", 2)
		parts := strings.SplitN(lines[0], "|", 2)
		statusLine = strings.TrimSpace(parts[0])
		if len(parts) > 1 {
			perfData = strings.TrimSpace(parts[1])
		}
		if len(lines) > 1 {
			longText = strings.TrimSpace(lines[1])
		}
	}

	eventLabels := map[string]string{
		"check":                            "exec::nagios",
		"target":                           command,
		types.AttrPrefix + "exit_code":     fmt.Sprintf("%d", exitCode),
	}
	if statusLine != "" {
		eventLabels[types.AttrPrefix+"status"] = statusLine
	}
	if perfData != "" {
		eventLabels[types.AttrPrefix+"perfdata"] = perfData
	}

	desc := statusLine
	if desc == "" {
		desc = fmt.Sprintf("exit code %d", exitCode)
	}
	if longText != "" {
		desc += "\n" + longText
	}

	event := types.BuildEvent(eventLabels).SetTitleRule("[check] [target]").
		SetEventStatus(status).
		SetDescription(desc)

	q.PushFront(event)
}

func nagiosExitCodeToStatus(code int) string {
	switch code {
	case 0:
		return types.EventStatusOk
	case 1:
		return types.EventStatusWarning
	case 2:
		return types.EventStatusCritical
	default:
		return types.EventStatusCritical
	}
}

func (ins *Instance) gatherJSON(q *safe.Queue[*types.Event], command string, stdout []byte, exitCode int) {
	if exitCode != 0 {
		q.PushFront(ins.buildErrorEvent(command,
			fmt.Errorf("non-zero exit code %d", exitCode), stdout))
		return
	}

	if len(stdout) == 0 {
		q.PushFront(ins.buildErrorEvent(command,
			fmt.Errorf("command produced no output"), nil))
		return
	}

	var events []*types.Event
	if err := json.Unmarshal(stdout, &events); err != nil {
		q.PushFront(ins.buildErrorEvent(command,
			fmt.Errorf("failed to parse JSON: %v", err), stdout))
		return
	}

	for i, e := range events {
		if e == nil {
			continue
		}
		if e.Labels == nil || e.Labels["check"] == "" {
			logger.Logger.Warnw("exec event missing 'check' label",
				"command", command, "index", i)
		}
		if e.EventStatus == "" {
			logger.Logger.Warnw("exec event missing 'event_status', defaulting to Critical",
				"command", command, "index", i)
			e.EventStatus = types.EventStatusCritical
		}
		q.PushFront(e)
	}
}

func combineOutput(stdout, stderr []byte) []byte {
	if len(stdout) == 0 {
		return stderr
	}
	if len(stderr) == 0 {
		return stdout
	}
	combined := make([]byte, 0, len(stdout)+1+len(stderr))
	combined = append(combined, stdout...)
	combined = append(combined, '\n')
	combined = append(combined, stderr...)
	return combined
}

func (ins *Instance) buildErrorEvent(command string, err error, output []byte) *types.Event {
	desc := fmt.Sprintf("command exec failed: %v", err)
	if len(output) > 0 {
		desc += "\n" + truncateUTF8(output, 1024)
	}

	return types.BuildEvent(map[string]string{
		"check":  "exec::error",
		"target": command,
	}).SetTitleRule("[check] [target]").
		SetEventStatus(types.EventStatusCritical).
		SetDescription(desc)
}

func truncateUTF8(b []byte, max int) string {
	if len(b) <= max {
		return string(b)
	}
	return strings.ToValidUTF8(string(b[:max]), "") + "... (truncated)"
}
