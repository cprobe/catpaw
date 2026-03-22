package redis

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/cprobe/catpaw/digcore/pkg/safe"
	"github.com/cprobe/catpaw/digcore/types"
)

func (ins *Instance) checkClusterState(q *safe.Queue[*types.Event], target string, info map[string]string, infoErr error) {
	event := ins.newEvent("redis::cluster_state", target)
	if infoErr != nil {
		q.PushFront(event.SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("failed to query redis cluster info: %v", infoErr)))
		return
	}

	state := strings.ToLower(strings.TrimSpace(info["cluster_state"]))
	if state == "" {
		q.PushFront(event.SetEventStatus(types.EventStatusCritical).
			SetDescription("redis cluster info missing cluster_state"))
		return
	}

	event.SetAttrs(map[string]string{
		"cluster_state":  state,
		"threshold_desc": fmt.Sprintf("%s: cluster_state must be ok", ins.ClusterState.Severity),
	}).SetCurrentValue(state)

	if state == "ok" {
		q.PushFront(event.SetDescription("redis cluster state is ok"))
		return
	}

	q.PushFront(event.SetEventStatus(ins.ClusterState.Severity).
		SetDescription(fmt.Sprintf("redis cluster state is %s, expected ok", state)))
}

func (ins *Instance) checkClusterTopology(q *safe.Queue[*types.Event], target string, info map[string]string, infoErr error, nodesRaw string, nodesErr error) {
	event := ins.newEvent("redis::cluster_topology", target)
	if infoErr != nil {
		q.PushFront(event.SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("failed to query redis cluster info: %v", infoErr)))
		return
	}
	if nodesErr != nil {
		q.PushFront(event.SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("failed to query redis cluster nodes: %v", nodesErr)))
		return
	}

	slotsAssigned, ok, err := infoGetInt(info, "cluster_slots_assigned")
	if err != nil {
		q.PushFront(event.SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("failed to parse redis cluster_slots_assigned: %v", err)))
		return
	}
	if !ok {
		q.PushFront(event.SetEventStatus(types.EventStatusCritical).
			SetDescription("redis cluster info missing cluster_slots_assigned"))
		return
	}
	slotsFail, ok, err := infoGetInt(info, "cluster_slots_fail")
	if err != nil {
		q.PushFront(event.SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("failed to parse redis cluster_slots_fail: %v", err)))
		return
	}
	if !ok {
		q.PushFront(event.SetEventStatus(types.EventStatusCritical).
			SetDescription("redis cluster info missing cluster_slots_fail"))
		return
	}

	failNodes, pfailNodes := summarizeClusterNodeFlags(nodesRaw)
	attrs := map[string]string{
		"fail_nodes":             strconv.Itoa(failNodes),
		"pfail_nodes":            strconv.Itoa(pfailNodes),
		"cluster_slots_assigned": strconv.Itoa(slotsAssigned),
		"cluster_slots_fail":     strconv.Itoa(slotsFail),
		"threshold_desc":         "Critical: fail nodes, slot gaps, or slots_fail > 0; Warning: pfail nodes",
	}
	event.SetAttrs(attrs)

	var criticalIssues []string
	var warningIssues []string
	if failNodes > 0 {
		criticalIssues = append(criticalIssues, fmt.Sprintf("%d fail node detected", failNodes))
	}
	if slotsFail > 0 {
		criticalIssues = append(criticalIssues, fmt.Sprintf("cluster_slots_fail is %d", slotsFail))
	}
	if slotsAssigned != clusterSlotsFull {
		criticalIssues = append(criticalIssues, fmt.Sprintf("assigned slots %d != expected %d", slotsAssigned, clusterSlotsFull))
	}
	if pfailNodes > 0 {
		warningIssues = append(warningIssues, fmt.Sprintf("%d pfail node detected", pfailNodes))
	}

	if len(criticalIssues) > 0 {
		q.PushFront(event.SetEventStatus(types.EventStatusCritical).
			SetDescription("redis cluster topology unhealthy: " + strings.Join(criticalIssues, "; ")))
		return
	}
	if len(warningIssues) > 0 {
		q.PushFront(event.SetEventStatus(types.EventStatusWarning).
			SetDescription("redis cluster topology unhealthy: " + strings.Join(warningIssues, "; ")))
		return
	}
	q.PushFront(event.SetDescription("redis cluster topology is healthy"))
}

func summarizeClusterNodeFlags(raw string) (failNodes int, pfailNodes int) {
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		flags := strings.Split(fields[2], ",")
		hasFail := false
		hasPFail := false
		for _, flag := range flags {
			switch strings.TrimSpace(flag) {
			case "fail":
				hasFail = true
			case "fail?", "pfail":
				hasPFail = true
			}
		}
		if hasFail {
			failNodes++
			continue
		}
		if hasPFail {
			pfailNodes++
		}
	}
	return failNodes, pfailNodes
}
