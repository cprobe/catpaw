package etcd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/cprobe/catpaw/digcore/diagnose"
	"github.com/cprobe/catpaw/digcore/plugins"
)

const (
	maxDiagBodySize  = 1 << 20  // 1MB
	maxRelevantLines = 500
	maxRelevantBytes = 64 << 10 // 64KB
)

var _ plugins.Diagnosable = (*EtcdPlugin)(nil)

// EtcdAccessor provides HTTP access to etcd members for diagnostic tools.
type EtcdAccessor struct {
	baseURL    string   // primary target (alert member)
	allTargets []string // all configured targets for cluster-wide tools
	client     *http.Client
}

func (a *EtcdAccessor) get(ctx context.Context, path string, maxBytes int64) ([]byte, error) {
	return a.getURL(ctx, a.baseURL+path, maxBytes)
}

func (a *EtcdAccessor) getURL(ctx context.Context, url string, maxBytes int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	return body, nil
}

// postJSON sends a POST with JSON body to the etcd gRPC gateway and returns the response.
func (a *EtcdAccessor) postJSON(ctx context.Context, path string, payload any, maxBytes int64) ([]byte, error) {
	return a.postJSONURL(ctx, a.baseURL+path, payload, maxBytes)
}

func (a *EtcdAccessor) postJSONURL(ctx context.Context, url string, payload any, maxBytes int64) ([]byte, error) {
	var bodyReader io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("marshal request: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	} else {
		bodyReader = bytes.NewReader([]byte("{}"))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bodyReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	return body, nil
}

// Close is a no-op; the HTTP client uses DisableKeepAlives.
func (a *EtcdAccessor) Close() error { return nil }

func (p *EtcdPlugin) RegisterDiagnoseTools(registry *diagnose.ToolRegistry) {
	registry.RegisterCategory("etcd", "etcd",
		"etcd diagnostic tools: health, cluster-wide status comparison, commit latency summary, alarms, member list, filtered metrics. "+
			"Tip: combine with system shell tools for deeper investigation: "+
			"process check (ps aux|grep etcd, ss -tlnp|grep 2379, netstat -tlnp|grep etcd), "+
			"startup flags (cat /proc/$(pgrep etcd)/cmdline|tr '\\0' '\\n' — shows --data-dir, --quota-backend-bytes, --auto-compaction-*), "+
			"disk I/O (iostat -xd 1 3, df -h <data-dir>), "+
			"system resources (top -bn1|head -20, free -h), "+
			"etcd logs (journalctl -u etcd --no-pager -n 100, or check log file path from process cmdline).",
		diagnose.ToolScopeRemote)

	registry.Register("etcd", diagnose.DiagnoseTool{
		Name:        "etcd_health",
		Description: "GET /health from the alert member, returns JSON body with health status",
		Scope:       diagnose.ToolScopeRemote,
		RemoteExecute: func(ctx context.Context, session *diagnose.DiagnoseSession, args map[string]string) (string, error) {
			acc, err := getEtcdAccessor(session)
			if err != nil {
				return "", err
			}
			body, err := acc.get(ctx, "/health", maxDiagBodySize)
			if err != nil {
				return "", fmt.Errorf("etcd_health: %w", err)
			}
			return string(body), nil
		},
	})

	registry.Register("etcd", diagnose.DiagnoseTool{
		Name:        "etcd_version",
		Description: "GET /version from the alert member, returns etcd server and cluster version",
		Scope:       diagnose.ToolScopeRemote,
		RemoteExecute: func(ctx context.Context, session *diagnose.DiagnoseSession, args map[string]string) (string, error) {
			acc, err := getEtcdAccessor(session)
			if err != nil {
				return "", err
			}
			body, err := acc.get(ctx, "/version", maxDiagBodySize)
			if err != nil {
				return "", fmt.Errorf("etcd_version: %w", err)
			}
			return string(body), nil
		},
	})

	registry.Register("etcd", diagnose.DiagnoseTool{
		Name:        "etcd_endpoint_status",
		Description: "POST /v3/maintenance/status on the alert member. Returns leader ID, raft index/term, DB size. For cluster-wide comparison use etcd_cluster_status instead.",
		Scope:       diagnose.ToolScopeRemote,
		RemoteExecute: func(ctx context.Context, session *diagnose.DiagnoseSession, args map[string]string) (string, error) {
			acc, err := getEtcdAccessor(session)
			if err != nil {
				return "", err
			}
			body, err := acc.postJSON(ctx, "/v3/maintenance/status", nil, maxDiagBodySize)
			if err != nil {
				return "", fmt.Errorf("etcd_endpoint_status: %w", err)
			}
			return prettyJSON(body), nil
		},
	})

	registry.Register("etcd", diagnose.DiagnoseTool{
		Name: "etcd_cluster_status",
		Description: "Query /v3/maintenance/status on ALL configured etcd members and output a comparison table: " +
			"endpoint, leader, dbSize, dbSizeInUse, fragmentation ratio, raftIndex, raftTerm. " +
			"Key for determining if slow commit is single-node or cluster-wide, and identifying DB bloat or fragmentation.",
		Scope: diagnose.ToolScopeRemote,
		RemoteExecute: func(ctx context.Context, session *diagnose.DiagnoseSession, args map[string]string) (string, error) {
			acc, err := getEtcdAccessor(session)
			if err != nil {
				return "", err
			}
			return acc.clusterStatus(ctx), nil
		},
	})

	registry.Register("etcd", diagnose.DiagnoseTool{
		Name: "etcd_commit_summary",
		Description: "Fetch /metrics from the alert member, parse and return a structured summary of commit-latency-related indicators: " +
			"backend_commit P50/P90/P99, wal_fsync P50/P90/P99, DB size, slow apply/read counts, compaction duration. " +
			"Much more token-efficient than etcd_metrics_relevant for commit-slow diagnosis.",
		Scope: diagnose.ToolScopeRemote,
		RemoteExecute: func(ctx context.Context, session *diagnose.DiagnoseSession, args map[string]string) (string, error) {
			acc, err := getEtcdAccessor(session)
			if err != nil {
				return "", err
			}
			return acc.commitSummary(ctx)
		},
	})

	registry.Register("etcd", diagnose.DiagnoseTool{
		Name:        "etcd_alarm_list",
		Description: "List active alarms (NOSPACE, CORRUPT) from the alert member. Empty alarms array means no active alarms.",
		Scope:       diagnose.ToolScopeRemote,
		RemoteExecute: func(ctx context.Context, session *diagnose.DiagnoseSession, args map[string]string) (string, error) {
			acc, err := getEtcdAccessor(session)
			if err != nil {
				return "", err
			}
			payload := map[string]interface{}{"action": 0, "memberID": 0}
			body, err := acc.postJSON(ctx, "/v3/maintenance/alarm", payload, maxDiagBodySize)
			if err != nil {
				return "", fmt.Errorf("etcd_alarm_list: %w", err)
			}
			return prettyJSON(body), nil
		},
	})

	registry.Register("etcd", diagnose.DiagnoseTool{
		Name:        "etcd_member_list",
		Description: "List cluster membership: member IDs, names, peer/client URLs, learner status. Key for diagnosing cluster topology and connectivity issues.",
		Scope:       diagnose.ToolScopeRemote,
		RemoteExecute: func(ctx context.Context, session *diagnose.DiagnoseSession, args map[string]string) (string, error) {
			acc, err := getEtcdAccessor(session)
			if err != nil {
				return "", err
			}
			body, err := acc.postJSON(ctx, "/v3/cluster/member/list", nil, maxDiagBodySize)
			if err != nil {
				return "", fmt.Errorf("etcd_member_list: %w", err)
			}
			return prettyJSON(body), nil
		},
	})

	registry.Register("etcd", diagnose.DiagnoseTool{
		Name:        "etcd_metrics_relevant",
		Description: "GET /metrics filtered to disk/server/mvcc/network/slow lines (max 500 lines / 64KB). Use for deep-dive when etcd_commit_summary is insufficient.",
		Scope:       diagnose.ToolScopeRemote,
		RemoteExecute: func(ctx context.Context, session *diagnose.DiagnoseSession, args map[string]string) (string, error) {
			acc, err := getEtcdAccessor(session)
			if err != nil {
				return "", err
			}
			body, err := acc.get(ctx, "/metrics", maxDiagBodySize)
			if err != nil {
				return "", fmt.Errorf("etcd_metrics_relevant: %w", err)
			}
			return filterRelevantMetrics(strings.NewReader(string(body))), nil
		},
	})

	registry.RegisterAccessorFactory("etcd", func(ctx context.Context, instanceRef any, target string) (any, error) {
		ins, ok := instanceRef.(*Instance)
		if !ok {
			return nil, fmt.Errorf("etcd accessor factory: expected *Instance, got %T", instanceRef)
		}
		if ins.client == nil {
			return nil, fmt.Errorf("etcd accessor factory: http client not initialized")
		}
		baseURL := ""
		if len(ins.Targets) > 0 {
			baseURL = ins.Targets[0]
		}
		return &EtcdAccessor{
			baseURL:    baseURL,
			allTargets: ins.Targets,
			client:     ins.client,
		}, nil
	})
}

// clusterStatus queries /v3/maintenance/status on every configured target
// and formats a comparison table.
func (a *EtcdAccessor) clusterStatus(ctx context.Context) string {
	if len(a.allTargets) == 0 {
		return "(no targets configured)"
	}

	type memberStatus struct {
		endpoint string
		raw      map[string]interface{}
		err      error
	}

	results := make([]memberStatus, len(a.allTargets))
	for i, target := range a.allTargets {
		results[i].endpoint = target
		body, err := a.postJSONURL(ctx, target+"/v3/maintenance/status", nil, maxDiagBodySize)
		if err != nil {
			results[i].err = err
			continue
		}
		var parsed map[string]interface{}
		if jsonErr := json.Unmarshal(body, &parsed); jsonErr != nil {
			results[i].err = jsonErr
			continue
		}
		results[i].raw = parsed
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%-40s %-10s %-14s %-14s %-8s %-14s %-10s %s\n",
		"ENDPOINT", "IS_LEADER", "DB_SIZE", "DB_IN_USE", "FRAG%", "RAFT_INDEX", "RAFT_TERM", "ERROR")
	b.WriteString(strings.Repeat("-", 120))
	b.WriteByte('\n')

	for _, r := range results {
		if r.err != nil {
			fmt.Fprintf(&b, "%-40s %-10s %-14s %-14s %-8s %-14s %-10s %s\n",
				r.endpoint, "-", "-", "-", "-", "-", "-", r.err.Error())
			continue
		}

		leader := jsonStr(r.raw, "leader")
		memberID := jsonStr(r.raw, "header", "member_id")
		isLeader := "no"
		if leader != "" && leader == memberID {
			isLeader = "YES"
		}

		dbSize := jsonFloat(r.raw, "dbSize")
		dbInUse := jsonFloat(r.raw, "dbSizeInUse")
		frag := ""
		if dbSize > 0 && dbInUse > 0 {
			fragPct := (1.0 - dbInUse/dbSize) * 100
			frag = fmt.Sprintf("%.1f%%", fragPct)
		}

		fmt.Fprintf(&b, "%-40s %-10s %-14s %-14s %-8s %-14s %-10s %s\n",
			r.endpoint,
			isLeader,
			humanBytes(dbSize),
			humanBytes(dbInUse),
			frag,
			jsonStr(r.raw, "raftIndex"),
			jsonStr(r.raw, "raftTerm"),
			"")
	}

	return b.String()
}

// commitSummary fetches /metrics and returns a structured summary of
// commit-latency-related indicators.
func (a *EtcdAccessor) commitSummary(ctx context.Context) (string, error) {
	body, err := a.get(ctx, "/metrics", maxDiagBodySize)
	if err != nil {
		return "", fmt.Errorf("fetch /metrics: %w", err)
	}
	reader := strings.NewReader(string(body))

	// We need to scan the full body multiple times for different metrics.
	// Parse all needed values in a single pass.
	lines := strings.Split(string(body), "\n")

	var b strings.Builder
	b.WriteString("=== etcd Commit Latency Summary ===\n\n")

	// 1. Backend commit histogram
	reader.Reset(string(body))
	if commitH, parseErr := parseHistogram(reader, "etcd_disk_backend_commit_duration_seconds", maxDiagBodySize); parseErr == nil {
		b.WriteString("[backend_commit latency]\n")
		writeQuantiles(&b, commitH, []float64{0.50, 0.90, 0.95, 0.99})
		if commitH.Count > 0 {
			avg := time.Duration(commitH.Sum / commitH.Count * float64(time.Second))
			fmt.Fprintf(&b, "  avg:     %s\n", formatDiagLatency(avg))
			fmt.Fprintf(&b, "  count:   %.0f\n", commitH.Count)
		}
		b.WriteByte('\n')
	} else {
		fmt.Fprintf(&b, "[backend_commit latency] not available: %v\n\n", parseErr)
	}

	// 2. WAL fsync histogram
	reader.Reset(string(body))
	if walH, parseErr := parseHistogram(reader, "etcd_disk_wal_fsync_duration_seconds", maxDiagBodySize); parseErr == nil {
		b.WriteString("[wal_fsync latency]\n")
		writeQuantiles(&b, walH, []float64{0.50, 0.90, 0.95, 0.99})
		if walH.Count > 0 {
			avg := time.Duration(walH.Sum / walH.Count * float64(time.Second))
			fmt.Fprintf(&b, "  avg:     %s\n", formatDiagLatency(avg))
			fmt.Fprintf(&b, "  count:   %.0f\n", walH.Count)
		}
		b.WriteByte('\n')
	} else {
		fmt.Fprintf(&b, "[wal_fsync latency] not available: %v\n\n", parseErr)
	}

	// 3. Key gauges and counters (single-pass extraction)
	gauges := map[string]float64{}
	wantGauges := map[string]bool{
		"etcd_mvcc_db_total_size_in_bytes":        true,
		"etcd_mvcc_db_total_size_in_use_in_bytes": true,
		"etcd_server_slow_apply_total":             true,
		"etcd_server_slow_read_indexes_total":      true,
		"etcd_server_proposals_failed_total":        true,
		"etcd_server_proposals_pending":             true,
		"etcd_server_leader_changes_seen_total":     true,
	}

	for _, line := range lines {
		if len(line) == 0 || line[0] == '#' {
			continue
		}
		for name := range wantGauges {
			if strings.HasPrefix(line, name+" ") || strings.HasPrefix(line, name+"{") {
				gauges[name] = parseMetricValue(line)
				break
			}
		}
	}

	b.WriteString("[storage]\n")
	if v, ok := gauges["etcd_mvcc_db_total_size_in_bytes"]; ok {
		fmt.Fprintf(&b, "  db_size:       %s\n", humanBytes(v))
	}
	if v, ok := gauges["etcd_mvcc_db_total_size_in_use_in_bytes"]; ok {
		fmt.Fprintf(&b, "  db_in_use:     %s\n", humanBytes(v))
	}
	if total, ok1 := gauges["etcd_mvcc_db_total_size_in_bytes"]; ok1 {
		if inUse, ok2 := gauges["etcd_mvcc_db_total_size_in_use_in_bytes"]; ok2 && total > 0 {
			fmt.Fprintf(&b, "  fragmentation: %.1f%%\n", (1.0-inUse/total)*100)
		}
	}
	b.WriteByte('\n')

	b.WriteString("[cluster indicators]\n")
	writeGauge(&b, gauges, "etcd_server_slow_apply_total", "slow_apply")
	writeGauge(&b, gauges, "etcd_server_slow_read_indexes_total", "slow_read_indexes")
	writeGauge(&b, gauges, "etcd_server_proposals_failed_total", "proposals_failed")
	writeGauge(&b, gauges, "etcd_server_proposals_pending", "proposals_pending")
	writeGauge(&b, gauges, "etcd_server_leader_changes_seen_total", "leader_changes")
	b.WriteByte('\n')

	b.WriteString("[diagnosis hints]\n")
	writeCommitHints(&b, gauges, body)

	return b.String(), nil
}

func writeQuantiles(b *strings.Builder, h *histogram, phis []float64) {
	for _, phi := range phis {
		val, err := histogramQuantile(phi, h)
		if err != nil || math.IsNaN(val) {
			fmt.Fprintf(b, "  p%-6s  N/A\n", quantileCheckLabel(phi)[1:])
			continue
		}
		latency := time.Duration(val * float64(time.Second))
		fmt.Fprintf(b, "  p%-6s  %s\n", quantileCheckLabel(phi)[1:], formatDiagLatency(latency))
	}
}

func writeGauge(b *strings.Builder, gauges map[string]float64, metric, label string) {
	if v, ok := gauges[metric]; ok {
		fmt.Fprintf(b, "  %-20s %.0f\n", label+":", v)
	}
}

func writeCommitHints(b *strings.Builder, gauges map[string]float64, body []byte) {
	hints := 0
	if v, ok := gauges["etcd_mvcc_db_total_size_in_bytes"]; ok && v > 2*1024*1024*1024 {
		fmt.Fprintf(b, "  - DB size > 2GB (%.1f GB): large DB increases commit latency, consider compaction + defrag\n", v/1024/1024/1024)
		hints++
	}
	if total, ok1 := gauges["etcd_mvcc_db_total_size_in_bytes"]; ok1 {
		if inUse, ok2 := gauges["etcd_mvcc_db_total_size_in_use_in_bytes"]; ok2 && total > 0 {
			fragPct := (1.0 - inUse/total) * 100
			if fragPct > 50 {
				fmt.Fprintf(b, "  - Fragmentation %.1f%%: defrag recommended (db_size=%.1fMB, in_use=%.1fMB)\n",
					fragPct, total/1024/1024, inUse/1024/1024)
				hints++
			}
		}
	}
	if v, ok := gauges["etcd_server_slow_apply_total"]; ok && v > 0 {
		fmt.Fprintf(b, "  - %.0f slow apply events detected: indicates raft proposal apply latency\n", v)
		hints++
	}
	if v, ok := gauges["etcd_server_leader_changes_seen_total"]; ok && v > 5 {
		fmt.Fprintf(b, "  - %.0f leader changes: frequent leader election suggests network/disk instability\n", v)
		hints++
	}
	if hints == 0 {
		b.WriteString("  (no obvious anomalies from metrics alone; check disk I/O via system tools)\n")
	}
	b.WriteString("  - Use shell to check: cat /proc/$(pgrep etcd)/cmdline | tr '\\0' ' ' for etcd startup flags\n")
	b.WriteString("  - Use disk I/O tools to check the device hosting etcd --data-dir\n")
}

func formatDiagLatency(d time.Duration) string {
	if d < time.Millisecond {
		return fmt.Sprintf("%.1f µs", float64(d)/float64(time.Microsecond))
	}
	if d < time.Second {
		return fmt.Sprintf("%.2f ms", float64(d)/float64(time.Millisecond))
	}
	return fmt.Sprintf("%.3f s", d.Seconds())
}

func prettyJSON(raw []byte) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return string(raw)
	}
	return buf.String()
}

// jsonStr extracts a string from a nested JSON map by key path.
func jsonStr(m map[string]interface{}, keys ...string) string {
	cur := m
	for i, k := range keys {
		v, ok := cur[k]
		if !ok {
			return ""
		}
		if i == len(keys)-1 {
			return fmt.Sprintf("%v", v)
		}
		next, ok := v.(map[string]interface{})
		if !ok {
			return ""
		}
		cur = next
	}
	return ""
}

func jsonFloat(m map[string]interface{}, key string) float64 {
	v, ok := m[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return n
	case json.Number:
		f, _ := n.Float64()
		return f
	default:
		return 0
	}
}

func humanBytes(b float64) string {
	if b <= 0 {
		return "0 B"
	}
	units := []string{"B", "KB", "MB", "GB", "TB"}
	i := 0
	for b >= 1024 && i < len(units)-1 {
		b /= 1024
		i++
	}
	return fmt.Sprintf("%.1f %s", b, units[i])
}

func getEtcdAccessor(session *diagnose.DiagnoseSession) (*EtcdAccessor, error) {
	if session.Accessor == nil {
		return nil, fmt.Errorf("no etcd accessor in session (remote connection not established)")
	}
	acc, ok := session.Accessor.(*EtcdAccessor)
	if !ok {
		return nil, fmt.Errorf("session accessor is %T, expected *EtcdAccessor", session.Accessor)
	}
	// Override baseURL to the actual alert target so single-node tools
	// query the right member, not just Targets[0].
	if alertTarget := session.Record.Alert.Target; alertTarget != "" && alertTarget != acc.baseURL {
		for _, t := range acc.allTargets {
			if t == alertTarget {
				acc.baseURL = alertTarget
				break
			}
		}
	}
	return acc, nil
}

var relevantPrefixes = []string{
	"etcd_disk_",
	"etcd_server_",
	"etcd_mvcc_",
	"etcd_debugging_",
	"etcd_network_peer_",
}

var relevantSubstrings = []string{
	"slow",
	"leader",
	"proposal",
	"snapshot",
	"wal",
	"compaction",
	"apply",
}

func filterRelevantMetrics(r io.Reader) string {
	scanner := bufio.NewScanner(r)
	var b strings.Builder
	lines := 0

	for scanner.Scan() {
		if lines >= maxRelevantLines || b.Len() >= maxRelevantBytes {
			fmt.Fprintf(&b, "\n... truncated at %d lines / %d bytes", lines, b.Len())
			break
		}
		line := scanner.Text()
		if isRelevantLine(line) {
			b.WriteString(line)
			b.WriteByte('\n')
			lines++
		}
	}

	if b.Len() == 0 {
		return "(no relevant etcd metrics found)"
	}
	return b.String()
}

func isRelevantLine(line string) bool {
	if len(line) == 0 {
		return false
	}
	if line[0] == '#' {
		for _, p := range relevantPrefixes {
			if strings.Contains(line, p) {
				return true
			}
		}
		for _, s := range relevantSubstrings {
			if strings.Contains(line, s) {
				return true
			}
		}
		return false
	}
	for _, p := range relevantPrefixes {
		if strings.HasPrefix(line, p) {
			return true
		}
	}
	for _, s := range relevantSubstrings {
		if strings.Contains(line, s) {
			return true
		}
	}
	return false
}
