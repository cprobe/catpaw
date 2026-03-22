package redis

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cprobe/catpaw/digcore/pkg/conv"
)

const (
	maxProbePeers    = 20
	peerProbeTimeout = 3 * time.Second
	probeConcurrency = 10
)

// clusterNodeEntry represents a parsed line from CLUSTER NODES output.
type clusterNodeEntry struct {
	ID        string
	Address   string // host:port (without @cport); empty for noaddr nodes
	Flags     []string
	Role      string // "master" or "slave"
	MasterID  string
	LinkState string // "connected" or "disconnected"
	IsSelf    bool
	IsFail    bool
	IsPFail   bool
	IsNoAddr  bool // node has no address (noaddr flag or :0 address)
}

// peerProbeResult holds the outcome of probing a single peer node.
type peerProbeResult struct {
	NodeID    string
	Address   string
	Role      string
	Reachable bool
	Latency   time.Duration
	Error     string
	Snapshot  string // key metrics summary
}

// parseClusterNodes parses the raw CLUSTER NODES output into structured entries.
// Nodes with noaddr/:0 are preserved with IsNoAddr=true (not silently dropped).
func parseClusterNodes(raw string) []clusterNodeEntry {
	var nodes []clusterNodeEntry
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 8 {
			continue
		}

		addr := fields[1]
		if idx := strings.Index(addr, "@"); idx >= 0 {
			addr = addr[:idx]
		}

		flags := strings.Split(fields[2], ",")
		var role string
		isSelf, isFail, isPFail, isNoAddr := false, false, false, false
		for _, f := range flags {
			switch f {
			case "master":
				role = "master"
			case "slave":
				role = "slave"
			case "myself":
				isSelf = true
			case "fail":
				isFail = true
			case "fail?", "pfail":
				isPFail = true
			case "noaddr":
				isNoAddr = true
			}
		}
		if addr == ":0" || addr == "" {
			isNoAddr = true
			addr = ""
		}
		if role == "" {
			role = "unknown"
		}

		nodes = append(nodes, clusterNodeEntry{
			ID:        fields[0],
			Address:   addr,
			Flags:     flags,
			Role:      role,
			MasterID:  fields[3],
			LinkState: fields[7],
			IsSelf:    isSelf,
			IsFail:    isFail,
			IsPFail:   isPFail,
			IsNoAddr:  isNoAddr,
		})
	}
	return nodes
}

// selectProbeCandidates picks which nodes to probe, respecting maxProbePeers.
// Priority: fail/pfail nodes first, then related replicas/masters, then others.
// Nodes with IsNoAddr are excluded from probing but kept in the full node list
// so formatPeerConnectivity can report them.
func selectProbeCandidates(nodes []clusterNodeEntry, selfAddr string) (probeable []clusterNodeEntry, unprobeable []clusterNodeEntry) {
	var selfNodeID string
	for _, n := range nodes {
		if n.IsSelf || (n.Address != "" && n.Address == selfAddr) {
			selfNodeID = n.ID
			break
		}
	}

	var priority, related, others []clusterNodeEntry
	for _, n := range nodes {
		if n.IsSelf || (n.Address != "" && n.Address == selfAddr) {
			continue
		}
		if n.IsNoAddr {
			unprobeable = append(unprobeable, n)
			continue
		}
		switch {
		case n.IsFail || n.IsPFail:
			priority = append(priority, n)
		case selfNodeID != "" && (n.MasterID == selfNodeID || n.ID == selfMasterID(nodes, selfNodeID)):
			related = append(related, n)
		default:
			others = append(others, n)
		}
	}

	sort.Slice(others, func(i, j int) bool { return others[i].ID < others[j].ID })

	probeable = append(probeable, priority...)
	probeable = append(probeable, related...)
	probeable = append(probeable, others...)
	if len(probeable) > maxProbePeers {
		probeable = probeable[:maxProbePeers]
	}
	return probeable, unprobeable
}

func selfMasterID(nodes []clusterNodeEntry, selfNodeID string) string {
	for _, n := range nodes {
		if n.ID == selfNodeID {
			return n.MasterID
		}
	}
	return ""
}

// probePeers concurrently probes peer nodes and returns results.
func probePeers(ctx context.Context, candidates []clusterNodeEntry, ins *Instance) []peerProbeResult {
	results := make([]peerProbeResult, len(candidates))
	sem := make(chan struct{}, probeConcurrency)
	var wg sync.WaitGroup

	for i, node := range candidates {
		wg.Add(1)
		go func(idx int, n clusterNodeEntry) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				results[idx] = peerProbeResult{
					NodeID:  n.ID,
					Address: n.Address,
					Role:    n.Role,
					Error:   "context cancelled",
				}
				return
			}
			results[idx] = probeSinglePeer(ctx, n, ins)
		}(i, node)
	}
	wg.Wait()
	return results
}

// probeSinglePeer connects to one peer, runs PING + lightweight INFO sections, and returns the result.
func probeSinglePeer(ctx context.Context, node clusterNodeEntry, ins *Instance) peerProbeResult {
	result := peerProbeResult{
		NodeID:  node.ID,
		Address: node.Address,
		Role:    node.Role,
	}

	probeCtx, cancel := context.WithTimeout(ctx, peerProbeTimeout)
	defer cancel()

	start := time.Now()
	acc, err := NewRedisAccessor(RedisAccessorConfig{
		Target:      node.Address,
		Username:    ins.Username,
		Password:    ins.Password,
		DB:          0,
		Timeout:     peerProbeTimeout,
		ReadTimeout: peerProbeTimeout,
		TLSConfig:   ins.tlsConfig,
		DialFunc:    contextDialFunc(probeCtx, ins.dialFunc),
	})
	if err != nil {
		result.Error = classifyConnError(err)
		return result
	}
	defer acc.Close()

	if err := acc.Ping(); err != nil {
		result.Error = classifyConnError(err)
		return result
	}
	result.Reachable = true
	result.Latency = time.Since(start)

	result.Snapshot = collectPeerSnapshot(acc)
	return result
}

// contextDialFunc wraps a dial function with context cancellation support.
func contextDialFunc(ctx context.Context, baseDial func(string, string) (net.Conn, error)) func(string, string) (net.Conn, error) {
	return func(network, address string) (net.Conn, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if baseDial != nil {
			return baseDial(network, address)
		}
		var d net.Dialer
		return d.DialContext(ctx, network, address)
	}
}

// collectPeerSnapshot runs targeted INFO sections to gather key metrics.
// Only fetches memory, clients, stats, replication — not INFO all.
func collectPeerSnapshot(acc *RedisAccessor) string {
	var parts []string

	if memInfo, err := acc.Info("memory"); err == nil {
		if n, _, err := infoGetInt64(memInfo, "used_memory"); err == nil && n > 0 {
			memStr := conv.HumanBytes(uint64(n))
			if maxmem, _, err := infoGetInt64(memInfo, "maxmemory"); err == nil && maxmem > 0 {
				memStr += "/" + conv.HumanBytes(uint64(maxmem))
			}
			parts = append(parts, "mem="+memStr)
		}
	}

	if clientInfo, err := acc.Info("clients"); err == nil {
		if v, ok := clientInfo["connected_clients"]; ok {
			parts = append(parts, "clients="+v)
		}
	}

	if statsInfo, err := acc.Info("stats"); err == nil {
		if v, ok := statsInfo["instantaneous_ops_per_sec"]; ok {
			parts = append(parts, "ops/s="+v)
		}
	}

	if replInfo, err := acc.Info("replication"); err == nil {
		if replInfo["role"] == "slave" {
			mOff, ok1, e1 := infoGetInt64(replInfo, "master_repl_offset")
			sOff, ok2, e2 := infoGetInt64(replInfo, "slave_repl_offset")
			if e1 == nil && e2 == nil && ok1 && ok2 {
				parts = append(parts, fmt.Sprintf("repl_lag=%d", mOff-sOff))
			}
		}
	}

	if len(parts) == 0 {
		return "ok"
	}
	return strings.Join(parts, ", ")
}

// classifyConnError categorizes a connection error for diagnostic clarity.
func classifyConnError(err error) string {
	s := err.Error()
	switch {
	case strings.Contains(s, "connection refused"):
		return "connection refused"
	case strings.Contains(s, "i/o timeout") || strings.Contains(s, "deadline exceeded"):
		return "timeout"
	case strings.Contains(s, "no route to host"):
		return "no route to host"
	case strings.Contains(s, "connection reset"):
		return "connection reset"
	case strings.Contains(s, "AUTH") || strings.Contains(s, "NOAUTH") || strings.Contains(s, "invalid password") ||
		strings.Contains(s, "WRONGPASS") || strings.Contains(s, "username-password"):
		return "auth failed"
	case strings.Contains(s, "context canceled"):
		return "context cancelled"
	default:
		if len(s) > 80 {
			s = s[:80] + "..."
		}
		return s
	}
}

// formatPeerConnectivity renders the [PEER CONNECTIVITY] section,
// including unprobeable (noaddr) nodes with explicit status.
func formatPeerConnectivity(results []peerProbeResult, unprobeable []clusterNodeEntry, totalNodes, probedCount int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[PEER CONNECTIVITY]\n")
	if probedCount < totalNodes-1-len(unprobeable) {
		fmt.Fprintf(&b, "(probed %d/%d peers, priority: fail/pfail + related replicas)\n", probedCount, totalNodes-1)
	}

	for _, r := range results {
		shortID := r.NodeID
		if len(shortID) > 8 {
			shortID = shortID[:8]
		}
		if r.Reachable {
			fmt.Fprintf(&b, "%s  %-21s  %-7s  ✓ %4s  %s\n",
				shortID, r.Address, r.Role,
				formatLatency(r.Latency), r.Snapshot)
		} else {
			fmt.Fprintf(&b, "%s  %-21s  %-7s  ✗       %s\n",
				shortID, r.Address, r.Role, r.Error)
		}
	}

	for _, n := range unprobeable {
		shortID := n.ID
		if len(shortID) > 8 {
			shortID = shortID[:8]
		}
		status := "noaddr"
		if n.IsFail {
			status = "fail,noaddr"
		} else if n.IsPFail {
			status = "pfail,noaddr"
		}
		fmt.Fprintf(&b, "%s  %-21s  %-7s  ✗       %s (unprobeable)\n",
			shortID, "(no address)", n.Role, status)
	}
	return b.String()
}

func formatLatency(d time.Duration) string {
	if d < time.Millisecond {
		return "<1ms"
	}
	return fmt.Sprintf("%dms", d.Milliseconds())
}

// peerCommandWhitelist defines the allowed read-only commands for redis_query_peer.
var peerCommandWhitelist = map[string]bool{
	"info":          true,
	"slowlog":       true,
	"client_list":   true,
	"cluster_info":  true,
	"memory_doctor": true,
	"memory_stats":  true,
	"latency":       true,
	"config_get":    true,
}

// executePeerCommand validates the peer address against the current cluster topology,
// creates a temporary connection, and runs one read-only command.
func executePeerCommand(ctx context.Context, ins *Instance, sessionAcc *RedisAccessor, peer, command, cmdArgs string) (string, error) {
	if !peerCommandWhitelist[command] {
		allowed := make([]string, 0, len(peerCommandWhitelist))
		for k := range peerCommandWhitelist {
			allowed = append(allowed, k)
		}
		sort.Strings(allowed)
		return "", fmt.Errorf("unsupported command %q, allowed: %s", command, strings.Join(allowed, ", "))
	}

	if err := validatePeerInTopology(sessionAcc, peer); err != nil {
		return "", err
	}

	peerCtx, cancel := context.WithTimeout(ctx, peerProbeTimeout)
	defer cancel()

	acc, err := NewRedisAccessor(RedisAccessorConfig{
		Target:      peer,
		Username:    ins.Username,
		Password:    ins.Password,
		DB:          0,
		Timeout:     peerProbeTimeout,
		ReadTimeout: peerProbeTimeout,
		TLSConfig:   ins.tlsConfig,
		DialFunc:    contextDialFunc(peerCtx, ins.dialFunc),
	})
	if err != nil {
		return "", fmt.Errorf("connect to peer %s: %s", peer, classifyConnError(err))
	}
	defer acc.Close()

	switch command {
	case "info":
		section := cmdArgs
		if section == "" {
			section = "all"
		}
		raw, err := acc.InfoRaw(section)
		if err != nil {
			return "", fmt.Errorf("peer %s INFO %s: %w", peer, section, err)
		}
		return fmt.Sprintf("[PEER %s] INFO %s\n%s", peer, section, raw), nil

	case "slowlog":
		count := parseIntArg(cmdArgs, 10)
		result, err := acc.SlowlogGet(count)
		if err != nil {
			return "", fmt.Errorf("peer %s SLOWLOG GET: %w", peer, err)
		}
		return fmt.Sprintf("[PEER %s] SLOWLOG GET %d\n%s", peer, count, result), nil

	case "client_list":
		return safeClientList(acc, peer)

	case "cluster_info":
		result, err := acc.ClusterInfo()
		if err != nil {
			return "", fmt.Errorf("peer %s CLUSTER INFO: %w", peer, err)
		}
		return fmt.Sprintf("[PEER %s] CLUSTER INFO\n%s", peer, result), nil

	case "memory_doctor":
		result, err := acc.Command("MEMORY", "DOCTOR")
		if err != nil {
			return "", fmt.Errorf("peer %s MEMORY DOCTOR: %w", peer, err)
		}
		return fmt.Sprintf("[PEER %s] MEMORY DOCTOR\n%s", peer, result), nil

	case "memory_stats":
		result, err := acc.Command("MEMORY", "STATS")
		if err != nil {
			return "", fmt.Errorf("peer %s MEMORY STATS: %w", peer, err)
		}
		return fmt.Sprintf("[PEER %s] MEMORY STATS\n%s", peer, result), nil

	case "latency":
		result, err := acc.Command("LATENCY", "LATEST")
		if err != nil {
			return "", fmt.Errorf("peer %s LATENCY LATEST: %w", peer, err)
		}
		if result == "" || result == "(nil)" {
			return fmt.Sprintf("[PEER %s] LATENCY LATEST\nNo latency events recorded.", peer), nil
		}
		return fmt.Sprintf("[PEER %s] LATENCY LATEST\n%s", peer, result), nil

	case "config_get":
		pattern := cmdArgs
		if pattern == "" {
			pattern = "*"
		}
		result, err := acc.ConfigGet(pattern)
		if err != nil {
			return "", fmt.Errorf("peer %s CONFIG GET %s: %w", peer, pattern, err)
		}
		return fmt.Sprintf("[PEER %s] CONFIG GET %s\n%s", peer, pattern, result), nil

	default:
		return "", fmt.Errorf("unhandled command %q", command)
	}
}

// validatePeerInTopology checks that the requested peer address exists in
// the current CLUSTER NODES topology. Prevents using redis_query_peer
// as an arbitrary Redis scanner.
func validatePeerInTopology(sessionAcc *RedisAccessor, peer string) error {
	if sessionAcc == nil {
		return fmt.Errorf("no session accessor available to validate peer topology")
	}
	nodesRaw, err := sessionAcc.ClusterNodes()
	if err != nil {
		return fmt.Errorf("cannot validate peer: CLUSTER NODES failed: %w", err)
	}
	nodes := parseClusterNodes(nodesRaw)
	for _, n := range nodes {
		if n.Address == peer {
			return nil
		}
	}
	return fmt.Errorf("peer %s is not in the current cluster topology, connection denied", peer)
}

const safeClientThreshold = 5000

// safeClientList runs CLIENT LIST on a peer with the same high-client-count
// protection as the main redis_client_list tool.
func safeClientList(acc *RedisAccessor, peer string) (string, error) {
	info, infoErr := acc.Info("clients")
	if infoErr == nil {
		if countStr, ok := info["connected_clients"]; ok {
			if count, parseErr := strconv.Atoi(countStr); parseErr == nil && count > safeClientThreshold {
				summary, err := clientSummaryFromInfo(acc, count)
				if err != nil {
					return "", fmt.Errorf("peer %s client summary: %w", peer, err)
				}
				return fmt.Sprintf("[PEER %s] CLIENT LIST (summary, %d clients)\n%s", peer, count, summary), nil
			}
		}
	}

	result, err := acc.ClientList()
	if err != nil {
		return "", fmt.Errorf("peer %s CLIENT LIST: %w", peer, err)
	}
	return fmt.Sprintf("[PEER %s] CLIENT LIST\n%s", peer, result), nil
}
