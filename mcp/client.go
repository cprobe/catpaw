package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
)

// Client communicates with one MCP server process over stdio (JSON-RPC 2.0).
type Client struct {
	name    string
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  *bufio.Reader
	stderr  strings.Builder
	mu      sync.Mutex // serializes request/response pairs
	nextID  atomic.Int64
	closed  atomic.Bool
}

// NewClient spawns an MCP server subprocess and returns a connected client.
func NewClient(ctx context.Context, name, command string, args []string, env map[string]string) (*Client, error) {
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Env = buildEnv(env)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp %s: stdin pipe: %w", name, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, fmt.Errorf("mcp %s: stdout pipe: %w", name, err)
	}

	c := &Client{
		name:   name,
		cmd:    cmd,
		stdin:  stdin,
		stdout: bufio.NewReaderSize(stdout, 256*1024),
	}
	cmd.Stderr = &c.stderr

	if err := cmd.Start(); err != nil {
		stdin.Close()
		return nil, fmt.Errorf("mcp %s: start %q: %w", name, command, err)
	}

	return c, nil
}

// Initialize performs the MCP handshake (initialize + notifications/initialized).
func (c *Client) Initialize(ctx context.Context) error {
	params := initializeParams{
		ProtocolVersion: protocolVersion,
		Capabilities:    clientCaps{},
		ClientInfo:      implementationID{Name: "catpaw", Version: "1.0"},
	}
	var result initializeResult
	if err := c.call(ctx, "initialize", params, &result); err != nil {
		return fmt.Errorf("mcp %s: initialize: %w", c.name, err)
	}

	return c.notify(ctx, "notifications/initialized", nil)
}

// ListTools calls tools/list and returns the available tools.
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
	var result toolsListResult
	if err := c.call(ctx, "tools/list", nil, &result); err != nil {
		return nil, fmt.Errorf("mcp %s: tools/list: %w", c.name, err)
	}
	return result.Tools, nil
}

// CallTool invokes a tool and returns the text result.
func (c *Client) CallTool(ctx context.Context, toolName string, args map[string]any) (string, error) {
	params := toolCallParams{
		Name:      toolName,
		Arguments: args,
	}
	var result toolCallResult
	if err := c.call(ctx, "tools/call", params, &result); err != nil {
		return "", fmt.Errorf("mcp %s: tools/call %s: %w", c.name, toolName, err)
	}

	if result.IsError {
		texts := extractTexts(result.Content)
		if texts != "" {
			return "", fmt.Errorf("mcp tool %s error: %s", toolName, texts)
		}
		return "", fmt.Errorf("mcp tool %s returned an error", toolName)
	}

	return extractTexts(result.Content), nil
}

// Close terminates the MCP server process.
func (c *Client) Close() error {
	if c.closed.Swap(true) {
		return nil
	}
	c.stdin.Close()
	return c.cmd.Wait()
}

// Name returns the configured server name.
func (c *Client) Name() string {
	return c.name
}

// Stderr returns any stderr output from the server process.
func (c *Client) Stderr() string {
	return c.stderr.String()
}

// --- internal ---

func (c *Client) call(ctx context.Context, method string, params, result any) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed.Load() {
		return fmt.Errorf("client closed")
	}

	id := int(c.nextID.Add(1))

	var rawParams json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("marshal params: %w", err)
		}
		rawParams = b
	}

	req := jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  rawParams,
	}

	if err := c.send(req); err != nil {
		return err
	}

	return c.recv(ctx, id, result)
}

func (c *Client) notify(_ context.Context, method string, params any) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed.Load() {
		return fmt.Errorf("client closed")
	}

	var rawParams json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("marshal params: %w", err)
		}
		rawParams = b
	}

	return c.send(jsonrpcRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  rawParams,
	})
}

func (c *Client) send(req jsonrpcRequest) error {
	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	data = append(data, '\n')
	_, err = c.stdin.Write(data)
	return err
}

func (c *Client) recv(ctx context.Context, expectedID int, result any) error {
	type readResult struct {
		line []byte
		err  error
	}
	ch := make(chan readResult, 1)

	// Goroutine reads from stdout until a matching response arrives.
	// On context cancellation this goroutine will eventually exit when
	// Close() kills the process and the pipe is closed.
	go func() {
		for {
			line, err := c.stdout.ReadBytes('\n')
			if err != nil {
				ch <- readResult{nil, err}
				return
			}
			line = trimLine(line)
			if len(line) == 0 {
				continue
			}
			// Skip server-initiated notifications (no "id" field)
			var peek struct {
				ID *json.RawMessage `json:"id"`
			}
			if json.Unmarshal(line, &peek) == nil && peek.ID == nil {
				continue
			}
			ch <- readResult{line, nil}
			return
		}
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case rr := <-ch:
		if rr.err != nil {
			return fmt.Errorf("read response: %w", rr.err)
		}
		var resp jsonrpcResponse
		if err := json.Unmarshal(rr.line, &resp); err != nil {
			return fmt.Errorf("unmarshal response: %w (raw: %s)", err, truncRaw(rr.line))
		}
		if resp.ID != expectedID {
			return fmt.Errorf("response id mismatch: got %d, want %d", resp.ID, expectedID)
		}
		if resp.Error != nil {
			return resp.Error
		}
		if result != nil && len(resp.Result) > 0 {
			return json.Unmarshal(resp.Result, result)
		}
		return nil
	}
}

func buildEnv(extra map[string]string) []string {
	env := os.Environ()
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}

func trimLine(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}

func extractTexts(contents []ToolContent) string {
	var parts []string
	for _, c := range contents {
		if c.Type == "text" && c.Text != "" {
			parts = append(parts, c.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func truncRaw(b []byte) string {
	if len(b) > 200 {
		return string(b[:200]) + "..."
	}
	return string(b)
}
