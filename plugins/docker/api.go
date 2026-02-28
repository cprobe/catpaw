package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// Docker Engine API response types — only the fields we need.

type containerListEntry struct {
	Id    string   `json:"Id"`
	Names []string `json:"Names"`
	Image string   `json:"Image"`
	State string   `json:"State"`
	// Status is human-readable, e.g. "Up 3 hours" or "Exited (137) 5 minutes ago"
	Status string `json:"Status"`
}

type containerInspect struct {
	State        containerState      `json:"State"`
	HostConfig   containerHostConfig `json:"HostConfig"`
	RestartCount int                 `json:"RestartCount"`
}

type containerState struct {
	Status     string `json:"Status"`
	Running    bool   `json:"Running"`
	Paused     bool   `json:"Paused"`
	Restarting bool   `json:"Restarting"`
	OOMKilled  bool   `json:"OOMKilled"`
	ExitCode   int    `json:"ExitCode"`
	StartedAt  string `json:"StartedAt"`
	FinishedAt string `json:"FinishedAt"`
	Health     *containerHealth `json:"Health,omitempty"`
}

type containerHealth struct {
	Status        string `json:"Status"`
	FailingStreak int    `json:"FailingStreak"`
	Log           []containerHealthLog `json:"Log"`
}

type containerHealthLog struct {
	Output string `json:"Output"`
}

type containerHostConfig struct {
	NanoCPUs  int64 `json:"NanoCpus"`
	CpuQuota  int64 `json:"CpuQuota"`
	CpuPeriod int64 `json:"CpuPeriod"`
}

type containerStats struct {
	CPUStats    cpuStats    `json:"cpu_stats"`
	PreCPUStats cpuStats    `json:"precpu_stats"`
	MemoryStats memoryStats `json:"memory_stats"`
}

type cpuStats struct {
	CPUUsage       cpuUsage       `json:"cpu_usage"`
	SystemCPUUsage uint64         `json:"system_cpu_usage"`
	OnlineCPUs     uint32         `json:"online_cpus"`
	ThrottlingData throttlingData `json:"throttling_data"`
}

type cpuUsage struct {
	TotalUsage  uint64   `json:"total_usage"`
	PercpuUsage []uint64 `json:"percpu_usage"`
}

type throttlingData struct {
	Periods          uint64 `json:"periods"`
	ThrottledPeriods uint64 `json:"throttled_periods"`
	ThrottledTime    uint64 `json:"throttled_time"`
}

type memoryStats struct {
	Usage uint64                 `json:"usage"`
	Limit uint64                 `json:"limit"`
	Stats map[string]interface{} `json:"stats"`
}

type versionResponse struct {
	APIVersion string `json:"ApiVersion"`
}

// newDockerHTTPClient creates an HTTP client for Docker Engine API.
// Unix socket paths produce a transport that dials the socket;
// http:// or https:// URLs use a standard transport.
func newDockerHTTPClient(socket string, timeout time.Duration) (*http.Client, string) {
	if strings.HasPrefix(socket, "http://") || strings.HasPrefix(socket, "https://") {
		return &http.Client{Timeout: timeout}, socket
	}

	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			d.Timeout = timeout
			return d.DialContext(ctx, "unix", socket)
		},
	}
	return &http.Client{Transport: transport, Timeout: timeout}, "http://localhost"
}

func (ins *Instance) apiURL(path string) string {
	return fmt.Sprintf("%s/v%s%s", ins.baseURL, ins.apiVersion, path)
}

func (ins *Instance) negotiateAPIVersion() {
	resp, err := ins.httpClient.Get(ins.baseURL + "/version")
	if err != nil {
		ins.apiVersion = "1.25"
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		ins.apiVersion = "1.25"
		return
	}

	var ver versionResponse
	if err := json.Unmarshal(body, &ver); err != nil || ver.APIVersion == "" {
		ins.apiVersion = "1.25"
		return
	}

	ins.apiVersion = ver.APIVersion
}

func (ins *Instance) listContainers() ([]containerListEntry, error) {
	resp, err := ins.httpClient.Get(ins.apiURL("/containers/json?all=true"))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(body), 200))
	}

	var containers []containerListEntry
	if err := json.NewDecoder(resp.Body).Decode(&containers); err != nil {
		return nil, fmt.Errorf("decode list response: %v", err)
	}
	return containers, nil
}

func (ins *Instance) inspectContainer(id string) (*containerInspect, error) {
	resp, err := ins.httpClient.Get(ins.apiURL("/containers/" + id + "/json"))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(body), 200))
	}

	var detail containerInspect
	if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil {
		return nil, fmt.Errorf("decode inspect response: %v", err)
	}
	return &detail, nil
}

func (ins *Instance) getContainerStats(id string) (*containerStats, error) {
	resp, err := ins.httpClient.Get(ins.apiURL("/containers/" + id + "/stats?stream=false"))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(body), 200))
	}

	var stats containerStats
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		return nil, fmt.Errorf("decode stats response: %v", err)
	}
	return &stats, nil
}

func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "..."
}
