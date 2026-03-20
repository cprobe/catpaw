package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/cprobe/catpaw/digcore/diagnose"
	"github.com/cprobe/catpaw/digcore/plugins"
)

var _ plugins.Diagnosable = (*DockerPlugin)(nil)

const (
	diagnoseTimeout    = 10 * time.Second
	defaultAPIVersion  = "1.25"
	maxDiagContainers  = 200
)

func (p *DockerPlugin) RegisterDiagnoseTools(registry *diagnose.ToolRegistry) {
	registry.RegisterCategory("docker", "docker",
		"Docker diagnostic tools (container list, stats). Requires Docker daemon access.",
		diagnose.ToolScopeLocal)

	registry.Register("docker", diagnose.DiagnoseTool{
		Name:        "docker_ps",
		Description: "List all containers with state and status (similar to docker ps -a)",
		Scope:       diagnose.ToolScopeLocal,
		Parameters: []diagnose.ToolParam{
			{Name: "socket", Type: "string", Description: "Docker socket path (default: /var/run/docker.sock on Linux, http://localhost:2375 on Windows)"},
		},
		Execute: execDockerPs,
	})

	registry.Register("docker", diagnose.DiagnoseTool{
		Name:        "docker_inspect",
		Description: "Show detailed state of a specific container (running, health, OOM, restarts, exit code)",
		Scope:       diagnose.ToolScopeLocal,
		Parameters: []diagnose.ToolParam{
			{Name: "name", Type: "string", Description: "Container name or ID", Required: true},
			{Name: "socket", Type: "string", Description: "Docker socket path"},
		},
		Execute: execDockerInspect,
	})
}

type diagClient struct {
	httpClient *http.Client
	baseURL    string
	apiVersion string
}

func newDiagClient(socket string) (*diagClient, error) {
	if socket == "" {
		if runtime.GOOS == "windows" {
			socket = "http://localhost:2375"
		} else {
			socket = "/var/run/docker.sock"
		}
	}
	if strings.Contains(socket, "..") {
		return nil, fmt.Errorf("socket path must not contain '..'")
	}

	client, baseURL := newDockerHTTPClient(socket, diagnoseTimeout)
	dc := &diagClient{httpClient: client, baseURL: baseURL, apiVersion: defaultAPIVersion}
	dc.negotiateVersion()
	return dc, nil
}

func (dc *diagClient) negotiateVersion() {
	resp, err := dc.httpClient.Get(dc.baseURL + "/version")
	if err != nil {
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return
	}
	var ver versionResponse
	if json.Unmarshal(body, &ver) == nil && ver.APIVersion != "" {
		dc.apiVersion = ver.APIVersion
	}
}

func (dc *diagClient) url(path string) string {
	return fmt.Sprintf("%s/v%s%s", dc.baseURL, dc.apiVersion, path)
}

func (dc *diagClient) list() ([]containerListEntry, error) {
	resp, err := dc.httpClient.Get(dc.url("/containers/json?all=true"))
	if err != nil {
		return nil, fmt.Errorf("docker API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("docker API HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var entries []containerListEntry
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&entries); err != nil {
		return nil, fmt.Errorf("decode container list: %w", err)
	}
	return entries, nil
}

func (dc *diagClient) inspect(id string) (*containerInspect, error) {
	resp, err := dc.httpClient.Get(dc.url("/containers/" + id + "/json"))
	if err != nil {
		return nil, fmt.Errorf("docker API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("docker API HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var detail containerInspect
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&detail); err != nil {
		return nil, fmt.Errorf("decode inspect: %w", err)
	}
	return &detail, nil
}

func execDockerPs(_ context.Context, args map[string]string) (string, error) {
	dc, err := newDiagClient(args["socket"])
	if err != nil {
		return "", err
	}

	containers, err := dc.list()
	if err != nil {
		return "", err
	}

	if len(containers) == 0 {
		return "No containers found.", nil
	}

	limit := len(containers)
	if limit > maxDiagContainers {
		limit = maxDiagContainers
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Total containers: %d\n\n", len(containers))
	fmt.Fprintf(&b, "%-14s  %-10s  %-30s  %s\n", "ID", "STATE", "STATUS", "NAME")
	for i := 0; i < limit; i++ {
		c := containers[i]
		fmt.Fprintf(&b, "%-14s  %-10s  %-30s  %s\n",
			shortContainerID(c.Id), c.State, c.Status, containerName(c))
	}
	if len(containers) > maxDiagContainers {
		fmt.Fprintf(&b, "\n...[showing %d of %d containers]\n", maxDiagContainers, len(containers))
	}
	return b.String(), nil
}

func execDockerInspect(_ context.Context, args map[string]string) (string, error) {
	name := strings.TrimSpace(args["name"])
	if name == "" {
		return "", fmt.Errorf("name parameter is required")
	}
	if err := validateContainerRef(name); err != nil {
		return "", err
	}

	dc, err := newDiagClient(args["socket"])
	if err != nil {
		return "", err
	}

	detail, err := dc.inspect(name)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Container:    %s\n", name)
	fmt.Fprintf(&b, "Status:       %s\n", detail.State.Status)
	fmt.Fprintf(&b, "Running:      %v\n", detail.State.Running)
	if detail.State.Paused {
		b.WriteString("Paused:       true\n")
	}
	if detail.State.Restarting {
		b.WriteString("Restarting:   true\n")
	}
	if detail.State.OOMKilled {
		b.WriteString("OOMKilled:    true\n")
	}
	if detail.State.ExitCode != 0 {
		fmt.Fprintf(&b, "ExitCode:     %d\n", detail.State.ExitCode)
	}
	if detail.RestartCount > 0 {
		fmt.Fprintf(&b, "RestartCount: %d\n", detail.RestartCount)
	}
	if detail.State.StartedAt != "" {
		fmt.Fprintf(&b, "StartedAt:    %s\n", detail.State.StartedAt)
	}
	if detail.State.FinishedAt != "" && detail.State.FinishedAt != "0001-01-01T00:00:00Z" {
		fmt.Fprintf(&b, "FinishedAt:   %s\n", detail.State.FinishedAt)
	}
	if h := detail.State.Health; h != nil {
		fmt.Fprintf(&b, "\nHealth:\n")
		fmt.Fprintf(&b, "  Status:        %s\n", h.Status)
		fmt.Fprintf(&b, "  FailingStreak: %d\n", h.FailingStreak)
		limit := len(h.Log)
		if limit > 3 {
			limit = 3
		}
		if limit > 0 {
			b.WriteString("  Recent checks:\n")
			for i := len(h.Log) - limit; i < len(h.Log); i++ {
				out := strings.TrimSpace(h.Log[i].Output)
				if len(out) > 200 {
					out = out[:200] + "..."
				}
				fmt.Fprintf(&b, "    %s\n", out)
			}
		}
	}
	return b.String(), nil
}

func validateContainerRef(name string) error {
	if len(name) > 128 {
		return fmt.Errorf("container name/ID too long (max 128)")
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf("container name must not contain '..'")
	}
	for _, r := range name {
		if r < 0x20 || r == ';' || r == '|' || r == '&' || r == '$' || r == '`' {
			return fmt.Errorf("container name contains invalid character %q", r)
		}
	}
	return nil
}
