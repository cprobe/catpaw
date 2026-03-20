package mcp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/cprobe/catpaw/digcore/config"
)

const (
	testInitTimeout  = 10 * time.Second
	testListTimeout  = 10 * time.Second
	testTotalTimeout = 60 * time.Second
)

// TestResult holds the result of testing one MCP server.
type TestResult struct {
	Name     string
	Status   string // PASS, FAIL
	Identity string
	Tools    []string
	Allowed  []string
	Message  string
	Elapsed  time.Duration
}

// RunTest connects to each configured MCP server, performs the handshake,
// discovers tools, and returns structured results. Each server is tested
// independently; failures are isolated.
func RunTest(mcpCfg config.MCPConfig) []TestResult {
	if !mcpCfg.Enabled {
		return []TestResult{{
			Name:    "(config)",
			Status:  "FAIL",
			Message: "[ai.mcp] enabled = false or not configured",
		}}
	}
	if len(mcpCfg.Servers) == 0 {
		return []TestResult{{
			Name:    "(config)",
			Status:  "FAIL",
			Message: "no servers configured under [[ai.mcp.servers]]",
		}}
	}

	ctx, cancel := context.WithTimeout(context.Background(), testTotalTimeout)
	defer cancel()

	builtins := config.HostBuiltins()
	results := make([]TestResult, 0, len(mcpCfg.Servers))
	for i := range mcpCfg.Servers {
		srv := &mcpCfg.Servers[i]
		r := testOneServer(ctx, srv, mcpCfg.DefaultIdentity, builtins)
		results = append(results, r)
	}
	return results
}

func testOneServer(ctx context.Context, srv *config.MCPServerConfig, defaultIdentity string, builtins map[string]string) TestResult {
	start := time.Now()
	r := TestResult{
		Name:     srv.Name,
		Identity: srv.ResolvedIdentity(defaultIdentity, builtins),
	}

	if srv.Name == "" {
		r.Status = "FAIL"
		r.Message = "server name is empty"
		r.Elapsed = time.Since(start)
		return r
	}
	if srv.Command == "" {
		r.Status = "FAIL"
		r.Message = "command is empty"
		r.Elapsed = time.Since(start)
		return r
	}

	client, err := NewClient(ctx, srv.Name, srv.Command, srv.Args, srv.Env)
	if err != nil {
		r.Status = "FAIL"
		r.Message = fmt.Sprintf("start failed: %v", err)
		r.Elapsed = time.Since(start)
		return r
	}
	defer client.Close()

	initCtx, initCancel := context.WithTimeout(ctx, testInitTimeout)
	defer initCancel()

	if err := client.Initialize(initCtx); err != nil {
		r.Status = "FAIL"
		r.Message = fmt.Sprintf("initialize failed: %v", err)
		appendStderr(&r, client)
		r.Elapsed = time.Since(start)
		return r
	}

	listCtx, listCancel := context.WithTimeout(ctx, testListTimeout)
	defer listCancel()

	tools, err := client.ListTools(listCtx)
	if err != nil {
		r.Status = "FAIL"
		r.Message = fmt.Sprintf("tools/list failed: %v", err)
		appendStderr(&r, client)
		r.Elapsed = time.Since(start)
		return r
	}

	r.Status = "PASS"
	for _, t := range tools {
		r.Tools = append(r.Tools, t.Name)
		if srv.IsToolAllowed(t.Name) {
			r.Allowed = append(r.Allowed, t.Name)
		}
	}
	r.Elapsed = time.Since(start)
	return r
}

// PrintTestResults prints formatted test results to stdout.
func PrintTestResults(results []TestResult) {
	fmt.Println("MCP Server Test Results")
	fmt.Println(strings.Repeat("─", 60))

	passCount, failCount := 0, 0
	for _, r := range results {
		switch r.Status {
		case "PASS":
			passCount++
		default:
			failCount++
		}

		statusColor := "\033[32m" // green
		if r.Status != "PASS" {
			statusColor = "\033[31m" // red
		}

		fmt.Printf("\n%s[%s]\033[0m %s", statusColor, r.Status, r.Name)
		if r.Elapsed > 0 {
			fmt.Printf("  (%s)", r.Elapsed.Round(time.Millisecond))
		}
		fmt.Println()

		if r.Identity != "" {
			fmt.Printf("  identity: %s\n", r.Identity)
		}

		if r.Status == "PASS" {
			fmt.Printf("  tools: %d discovered", len(r.Tools))
			if len(r.Allowed) < len(r.Tools) {
				fmt.Printf(", %d allowed by whitelist", len(r.Allowed))
			}
			fmt.Println()
			for _, name := range r.Allowed {
				fmt.Printf("    ✓ %s\n", name)
			}
		} else {
			fmt.Printf("  error: %s\n", r.Message)
		}
	}

	fmt.Println()
	fmt.Println(strings.Repeat("─", 60))
	fmt.Printf("Total: %d server(s), ", len(results))
	if failCount == 0 {
		fmt.Printf("\033[32m%d passed\033[0m\n", passCount)
	} else {
		fmt.Printf("\033[32m%d passed\033[0m, \033[31m%d failed\033[0m\n", passCount, failCount)
	}
}

func appendStderr(r *TestResult, client *Client) {
	stderr := strings.TrimSpace(client.Stderr())
	if stderr != "" {
		r.Message += "\nstderr:\n" + stderr
	}
}
