package redis

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/cprobe/catpaw/digcore/pkg/conv"
	"github.com/cprobe/catpaw/digcore/pkg/safe"
	"github.com/cprobe/catpaw/digcore/types"
)

func (ins *Instance) checkResponseTime(q *safe.Queue[*types.Event], target string, responseTime time.Duration) {
	if ins.ResponseTime.WarnGe == 0 && ins.ResponseTime.CriticalGe == 0 {
		return
	}

	var parts []string
	if ins.ResponseTime.WarnGe > 0 {
		parts = append(parts, fmt.Sprintf("Warning ≥ %s", time.Duration(ins.ResponseTime.WarnGe).String()))
	}
	if ins.ResponseTime.CriticalGe > 0 {
		parts = append(parts, fmt.Sprintf("Critical ≥ %s", time.Duration(ins.ResponseTime.CriticalGe).String()))
	}
	attrs := map[string]string{
		"response_time":  responseTime.String(),
		"threshold_desc": strings.Join(parts, ", "),
	}
	event := ins.newEvent("redis::response_time", target).SetAttrs(attrs).SetCurrentValue(responseTime.String())

	status := types.EvaluateGeThreshold(float64(responseTime), float64(ins.ResponseTime.WarnGe), float64(ins.ResponseTime.CriticalGe))
	switch status {
	case types.EventStatusCritical:
		q.PushFront(event.SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("redis response time %s >= critical threshold %s",
				responseTime, time.Duration(ins.ResponseTime.CriticalGe))))
	case types.EventStatusWarning:
		q.PushFront(event.SetEventStatus(types.EventStatusWarning).
			SetDescription(fmt.Sprintf("redis response time %s >= warning threshold %s",
				responseTime, time.Duration(ins.ResponseTime.WarnGe))))
	default:
		q.PushFront(event.SetDescription(fmt.Sprintf("redis response time %s, everything is ok", responseTime)))
	}
}

func (ins *Instance) checkRole(q *safe.Queue[*types.Event], target string, info map[string]string) {
	event := ins.newEvent("redis::role", target)
	actual, ok := info["role"]
	if !ok {
		q.PushFront(event.SetEventStatus(types.EventStatusCritical).
			SetDescription("redis info output missing role"))
		return
	}

	actual = strings.ToLower(strings.TrimSpace(actual))
	event.SetAttrs(map[string]string{
		"actual":         actual,
		"expect":         ins.Role.Expect,
		"threshold_desc": fmt.Sprintf("%s: role ≠ %s", ins.Role.Severity, ins.Role.Expect),
	}).SetCurrentValue(actual)

	if actual == ins.Role.Expect {
		q.PushFront(event.SetDescription(fmt.Sprintf("redis role is %s, matches expectation", actual)))
		return
	}

	q.PushFront(event.SetEventStatus(ins.Role.Severity).
		SetDescription(fmt.Sprintf("redis role is %s, expected %s", actual, ins.Role.Expect)))
}

func (ins *Instance) checkMasterLink(q *safe.Queue[*types.Event], target string, info map[string]string) {
	event := ins.newEvent("redis::master_link_status", target)
	attrs := map[string]string{
		"threshold_desc": fmt.Sprintf("%s: master link status does not match expected", ins.MasterLink.Severity),
	}
	if role, ok := info["role"]; ok {
		attrs["role"] = role
	}
	actual, ok := info["master_link_status"]
	if !ok {
		q.PushFront(event.SetEventStatus(ins.MasterLink.Severity).
			SetDescription("redis replication info missing master_link_status"))
		return
	}

	actual = strings.ToLower(strings.TrimSpace(actual))
	attrs["actual"] = actual
	attrs["expect"] = ins.MasterLink.Expect
	if v, ok := info["master_host"]; ok && v != "" {
		attrs["master_host"] = v
	}
	if v, ok := info["master_port"]; ok && v != "" {
		attrs["master_port"] = v
	}
	event.SetAttrs(attrs).SetCurrentValue(actual)

	if actual == ins.MasterLink.Expect {
		q.PushFront(event.SetDescription(fmt.Sprintf("redis master link status is %s, matches expectation", actual)))
		return
	}

	q.PushFront(event.SetEventStatus(ins.MasterLink.Severity).
		SetDescription(fmt.Sprintf("redis master link status is %s, expected %s", actual, ins.MasterLink.Expect)))
}

func (ins *Instance) checkCount(q *safe.Queue[*types.Event], target, check string, value int, thresholds CountCheck, metricName string) {
	labelKey := strings.TrimPrefix(check, "redis::")
	var parts []string
	if thresholds.WarnGe > 0 {
		parts = append(parts, fmt.Sprintf("Warning ≥ %d", thresholds.WarnGe))
	}
	if thresholds.CriticalGe > 0 {
		parts = append(parts, fmt.Sprintf("Critical ≥ %d", thresholds.CriticalGe))
	}
	attrs := map[string]string{
		labelKey:         strconv.Itoa(value),
		"threshold_desc": strings.Join(parts, ", "),
	}
	event := ins.newEvent(check, target).SetAttrs(attrs).SetCurrentValue(strconv.Itoa(value))

	status := types.EvaluateGeThreshold(float64(value), float64(thresholds.WarnGe), float64(thresholds.CriticalGe))
	switch status {
	case types.EventStatusCritical:
		q.PushFront(event.SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("redis %s %d >= critical threshold %d", metricName, value, thresholds.CriticalGe)))
	case types.EventStatusWarning:
		q.PushFront(event.SetEventStatus(types.EventStatusWarning).
			SetDescription(fmt.Sprintf("redis %s %d >= warning threshold %d", metricName, value, thresholds.WarnGe)))
	default:
		q.PushFront(event.SetDescription(fmt.Sprintf("redis %s %d, everything is ok", metricName, value)))
	}
}

func (ins *Instance) checkCountFromInfo(q *safe.Queue[*types.Event], target, check string, info map[string]string, key string, thresholds CountCheck, metricName string) {
	value, ok, err := infoGetInt(info, key)
	if err != nil {
		q.PushFront(ins.newEvent(check, target).
			SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("failed to parse redis %s: %v", key, err)))
		return
	}
	if !ok {
		q.PushFront(ins.newEvent(check, target).
			SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("redis info output missing %s", key)))
		return
	}
	ins.checkCount(q, target, check, value, thresholds, metricName)
}

func (ins *Instance) checkMinCountFromInfo(q *safe.Queue[*types.Event], target, check string, info map[string]string, key string, thresholds MinCountCheck, metricName string) {
	value, ok, err := infoGetInt(info, key)
	if err != nil {
		q.PushFront(ins.newEvent(check, target).
			SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("failed to parse redis %s: %v", key, err)))
		return
	}
	if !ok {
		q.PushFront(ins.newEvent(check, target).
			SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("redis info output missing %s", key)))
		return
	}
	ins.checkMinCount(q, target, check, value, thresholds, metricName)
}

func (ins *Instance) checkMinCount(q *safe.Queue[*types.Event], target, check string, value int, thresholds MinCountCheck, metricName string) {
	labelKey := strings.TrimPrefix(check, "redis::")
	var parts []string
	if thresholds.WarnLt > 0 {
		parts = append(parts, fmt.Sprintf("Warning < %d", thresholds.WarnLt))
	}
	if thresholds.CriticalLt > 0 {
		parts = append(parts, fmt.Sprintf("Critical < %d", thresholds.CriticalLt))
	}
	attrs := map[string]string{
		labelKey:         strconv.Itoa(value),
		"threshold_desc": strings.Join(parts, ", "),
	}
	event := ins.newEvent(check, target).SetAttrs(attrs).SetCurrentValue(strconv.Itoa(value))

	if thresholds.CriticalLt > 0 && value < thresholds.CriticalLt {
		q.PushFront(event.SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("redis %s %d < critical threshold %d", metricName, value, thresholds.CriticalLt)))
		return
	}
	if thresholds.WarnLt > 0 && value < thresholds.WarnLt {
		q.PushFront(event.SetEventStatus(types.EventStatusWarning).
			SetDescription(fmt.Sprintf("redis %s %d < warning threshold %d", metricName, value, thresholds.WarnLt)))
		return
	}
	q.PushFront(event.SetDescription(fmt.Sprintf("redis %s %d, everything is ok", metricName, value)))
}

func (ins *Instance) checkReplLag(q *safe.Queue[*types.Event], target string, info map[string]string) {
	event := ins.newEvent("redis::repl_lag", target)
	role := strings.ToLower(strings.TrimSpace(info["role"]))
	if role == "" {
		q.PushFront(event.SetEventStatus(types.EventStatusCritical).
			SetDescription("redis replication info missing role"))
		return
	}

	lag, desc, attrs, err := calculateReplicationLag(info)
	if err != nil {
		q.PushFront(event.SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("failed to evaluate redis replication lag: %v", err)))
		return
	}
	if lag < 0 {
		if attrs == nil {
			attrs = map[string]string{}
		}
		attrs["role"] = role
		attrs["threshold_desc"] = fmt.Sprintf("Warning ≥ %s, Critical ≥ %s", ins.ReplLag.WarnGe.String(), ins.ReplLag.CriticalGe.String())
		q.PushFront(event.SetAttrs(attrs).SetDescription(desc))
		return
	}

	if attrs == nil {
		attrs = map[string]string{}
	}
	attrs["role"] = role
	attrs["repl_lag_bytes"] = strconv.FormatInt(lag, 10)
	attrs["repl_lag"] = conv.HumanBytes(uint64(lag))
	var parts []string
	if ins.ReplLag.WarnGe > 0 {
		parts = append(parts, fmt.Sprintf("Warning ≥ %s", ins.ReplLag.WarnGe.String()))
	}
	if ins.ReplLag.CriticalGe > 0 {
		parts = append(parts, fmt.Sprintf("Critical ≥ %s", ins.ReplLag.CriticalGe.String()))
	}
	attrs["threshold_desc"] = strings.Join(parts, ", ")
	event.SetAttrs(attrs).SetCurrentValue(conv.HumanBytes(uint64(lag)))

	status := types.EvaluateGeThreshold(float64(lag), float64(ins.ReplLag.WarnGe), float64(ins.ReplLag.CriticalGe))
	switch status {
	case types.EventStatusCritical:
		q.PushFront(event.SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("redis replication lag %s >= critical threshold %s",
				conv.HumanBytes(uint64(lag)), ins.ReplLag.CriticalGe.String())))
	case types.EventStatusWarning:
		q.PushFront(event.SetEventStatus(types.EventStatusWarning).
			SetDescription(fmt.Sprintf("redis replication lag %s >= warning threshold %s",
				conv.HumanBytes(uint64(lag)), ins.ReplLag.WarnGe.String())))
	default:
		q.PushFront(event.SetDescription(desc))
	}
}

func (ins *Instance) checkCounterDeltas(q *safe.Queue[*types.Event], target string, info map[string]string) {
	var (
		evicted  uint64
		expired  uint64
		rejected uint64
	)

	if ins.EvictedKeys.WarnGe > 0 || ins.EvictedKeys.CriticalGe > 0 {
		value, ok, err := infoGetUint64(info, "evicted_keys")
		if err != nil {
			q.PushFront(ins.newEvent("redis::evicted_keys", target).
				SetEventStatus(types.EventStatusCritical).
				SetDescription(fmt.Sprintf("failed to parse redis evicted_keys: %v", err)))
			return
		}
		if !ok {
			q.PushFront(ins.newEvent("redis::evicted_keys", target).
				SetEventStatus(types.EventStatusCritical).
				SetDescription("redis info output missing evicted_keys"))
			return
		}
		evicted = value
	}

	if ins.ExpiredKeys.WarnGe > 0 || ins.ExpiredKeys.CriticalGe > 0 {
		value, ok, err := infoGetUint64(info, "expired_keys")
		if err != nil {
			q.PushFront(ins.newEvent("redis::expired_keys", target).
				SetEventStatus(types.EventStatusCritical).
				SetDescription(fmt.Sprintf("failed to parse redis expired_keys: %v", err)))
			return
		}
		if !ok {
			q.PushFront(ins.newEvent("redis::expired_keys", target).
				SetEventStatus(types.EventStatusCritical).
				SetDescription("redis info output missing expired_keys"))
			return
		}
		expired = value
	}

	if ins.RejectedConn.WarnGe > 0 || ins.RejectedConn.CriticalGe > 0 {
		value, ok, err := infoGetUint64(info, "rejected_connections")
		if err != nil {
			q.PushFront(ins.newEvent("redis::rejected_connections", target).
				SetEventStatus(types.EventStatusCritical).
				SetDescription(fmt.Sprintf("failed to parse redis rejected_connections: %v", err)))
			return
		}
		if !ok {
			q.PushFront(ins.newEvent("redis::rejected_connections", target).
				SetEventStatus(types.EventStatusCritical).
				SetDescription("redis info output missing rejected_connections"))
			return
		}
		rejected = value
	}

	ins.statsMu.Lock()
	prev := ins.prevStats[target]
	initialized := ins.initialized[target]
	ins.prevStats[target] = redisCounterSnapshot{
		evictedKeys:  evicted,
		expiredKeys:  expired,
		rejectedConn: rejected,
	}
	ins.initialized[target] = true
	ins.statsMu.Unlock()

	if !initialized {
		if ins.EvictedKeys.WarnGe > 0 || ins.EvictedKeys.CriticalGe > 0 {
			event := ins.newEvent("redis::evicted_keys", target).SetAttrs(map[string]string{
				"delta": "0",
				"total": strconv.FormatUint(evicted, 10),
			})
			q.PushFront(event.SetDescription(fmt.Sprintf("redis evicted keys baseline established (total: %d)", evicted)))
		}
		if ins.ExpiredKeys.WarnGe > 0 || ins.ExpiredKeys.CriticalGe > 0 {
			event := ins.newEvent("redis::expired_keys", target).SetAttrs(map[string]string{
				"delta": "0",
				"total": strconv.FormatUint(expired, 10),
			})
			q.PushFront(event.SetDescription(fmt.Sprintf("redis expired keys baseline established (total: %d)", expired)))
		}
		if ins.RejectedConn.WarnGe > 0 || ins.RejectedConn.CriticalGe > 0 {
			event := ins.newEvent("redis::rejected_connections", target).SetAttrs(map[string]string{
				"delta": "0",
				"total": strconv.FormatUint(rejected, 10),
			})
			q.PushFront(event.SetDescription(fmt.Sprintf("redis rejected connections baseline established (total: %d)", rejected)))
		}
		return
	}

	if ins.EvictedKeys.WarnGe > 0 || ins.EvictedKeys.CriticalGe > 0 {
		delta := uint64(0)
		if evicted >= prev.evictedKeys {
			delta = evicted - prev.evictedKeys
		}
		ins.checkDeltaCount(q, target, "redis::evicted_keys", delta, evicted, ins.EvictedKeys, "evicted keys")
	}

	if ins.ExpiredKeys.WarnGe > 0 || ins.ExpiredKeys.CriticalGe > 0 {
		delta := uint64(0)
		if expired >= prev.expiredKeys {
			delta = expired - prev.expiredKeys
		}
		ins.checkDeltaCount(q, target, "redis::expired_keys", delta, expired, ins.ExpiredKeys, "expired keys")
	}

	if ins.RejectedConn.WarnGe > 0 || ins.RejectedConn.CriticalGe > 0 {
		delta := uint64(0)
		if rejected >= prev.rejectedConn {
			delta = rejected - prev.rejectedConn
		}
		ins.checkDeltaCount(q, target, "redis::rejected_connections", delta, rejected, ins.RejectedConn, "rejected connections")
	}
}

func (ins *Instance) checkDeltaCount(q *safe.Queue[*types.Event], target, check string, delta, total uint64, thresholds CountCheck, metricName string) {
	var parts []string
	if thresholds.WarnGe > 0 {
		parts = append(parts, fmt.Sprintf("Warning ≥ %d", thresholds.WarnGe))
	}
	if thresholds.CriticalGe > 0 {
		parts = append(parts, fmt.Sprintf("Critical ≥ %d", thresholds.CriticalGe))
	}
	attrs := map[string]string{
		"delta":          strconv.FormatUint(delta, 10),
		"total":          strconv.FormatUint(total, 10),
		"threshold_desc": strings.Join(parts, ", "),
	}
	event := ins.newEvent(check, target).SetAttrs(attrs).SetCurrentValue(strconv.FormatUint(delta, 10))

	status := types.EvaluateGeThreshold(float64(delta), float64(thresholds.WarnGe), float64(thresholds.CriticalGe))
	switch status {
	case types.EventStatusCritical:
		q.PushFront(event.SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("redis %s delta %d >= critical threshold %d", metricName, delta, thresholds.CriticalGe)))
	case types.EventStatusWarning:
		q.PushFront(event.SetEventStatus(types.EventStatusWarning).
			SetDescription(fmt.Sprintf("redis %s delta %d >= warning threshold %d", metricName, delta, thresholds.WarnGe)))
	default:
		q.PushFront(event.SetDescription(fmt.Sprintf("redis %s delta %d, everything is ok", metricName, delta)))
	}
}

func (ins *Instance) checkUsedMemory(q *safe.Queue[*types.Event], target string, info map[string]string) {
	event := ins.newEvent("redis::used_memory", target)
	usedMemory, ok, err := infoGetInt64(info, "used_memory")
	if err != nil {
		q.PushFront(event.SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("failed to parse redis used_memory: %v", err)))
		return
	}
	if !ok {
		q.PushFront(event.SetEventStatus(types.EventStatusCritical).
			SetDescription("redis info output missing used_memory"))
		return
	}

	var memParts []string
	if ins.UsedMemory.WarnGe > 0 {
		memParts = append(memParts, fmt.Sprintf("Warning ≥ %s", ins.UsedMemory.WarnGe.String()))
	}
	if ins.UsedMemory.CriticalGe > 0 {
		memParts = append(memParts, fmt.Sprintf("Critical ≥ %s", ins.UsedMemory.CriticalGe.String()))
	}
	attrs := map[string]string{
		"used_memory":       conv.HumanBytes(uint64(usedMemory)),
		"used_memory_bytes": strconv.FormatInt(usedMemory, 10),
		"threshold_desc":    strings.Join(memParts, ", "),
	}
	if maxmemory, ok, err := infoGetInt64(info, "maxmemory"); err == nil && ok && maxmemory > 0 {
		attrs["maxmemory"] = conv.HumanBytes(uint64(maxmemory))
		attrs["maxmemory_bytes"] = strconv.FormatInt(maxmemory, 10)
		attrs["used_percent_of_maxmemory"] = fmt.Sprintf("%.1f%%", float64(usedMemory)*100/float64(maxmemory))
	}
	event.SetAttrs(attrs).SetCurrentValue(conv.HumanBytes(uint64(usedMemory)))

	status := types.EvaluateGeThreshold(float64(usedMemory), float64(ins.UsedMemory.WarnGe), float64(ins.UsedMemory.CriticalGe))
	switch status {
	case types.EventStatusCritical:
		q.PushFront(event.SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("redis used memory %s >= critical threshold %s",
				conv.HumanBytes(uint64(usedMemory)), ins.UsedMemory.CriticalGe.String())))
	case types.EventStatusWarning:
		q.PushFront(event.SetEventStatus(types.EventStatusWarning).
			SetDescription(fmt.Sprintf("redis used memory %s >= warning threshold %s",
				conv.HumanBytes(uint64(usedMemory)), ins.UsedMemory.WarnGe.String())))
	default:
		q.PushFront(event.SetDescription(fmt.Sprintf("redis used memory %s, everything is ok", conv.HumanBytes(uint64(usedMemory)))))
	}
}

func (ins *Instance) checkUsedMemoryPct(q *safe.Queue[*types.Event], target string, info map[string]string) {
	event := ins.newEvent("redis::used_memory_pct", target)
	usedMemory, ok, err := infoGetInt64(info, "used_memory")
	if err != nil {
		q.PushFront(event.SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("failed to parse redis used_memory: %v", err)))
		return
	}
	if !ok {
		q.PushFront(event.SetEventStatus(types.EventStatusCritical).
			SetDescription("redis info output missing used_memory"))
		return
	}

	maxMemory, ok, err := infoGetInt64(info, "maxmemory")
	if err != nil {
		q.PushFront(event.SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("failed to parse redis maxmemory: %v", err)))
		return
	}
	if !ok {
		q.PushFront(event.SetEventStatus(types.EventStatusCritical).
			SetDescription("redis info output missing maxmemory"))
		return
	}
	if maxMemory <= 0 {
		q.PushFront(event.SetAttrs(map[string]string{
			"used_memory":       conv.HumanBytes(uint64(usedMemory)),
			"used_memory_bytes": strconv.FormatInt(usedMemory, 10),
			"maxmemory_bytes":   "0",
			"threshold_desc":    fmt.Sprintf("Warning ≥ %d%%, Critical ≥ %d%%", ins.UsedMemoryPct.WarnGe, ins.UsedMemoryPct.CriticalGe),
		}).SetDescription("redis maxmemory is 0 (unlimited), used memory percent check skipped"))
		return
	}

	percent := float64(usedMemory) * 100 / float64(maxMemory)
	attrs := map[string]string{
		"used_memory":       conv.HumanBytes(uint64(usedMemory)),
		"used_memory_bytes": strconv.FormatInt(usedMemory, 10),
		"maxmemory":         conv.HumanBytes(uint64(maxMemory)),
		"maxmemory_bytes":   strconv.FormatInt(maxMemory, 10),
		"used_memory_pct":   fmt.Sprintf("%.1f%%", percent),
		"threshold_desc":    fmt.Sprintf("Warning ≥ %d%%, Critical ≥ %d%%", ins.UsedMemoryPct.WarnGe, ins.UsedMemoryPct.CriticalGe),
	}
	event.SetAttrs(attrs).SetCurrentValue(fmt.Sprintf("%.1f%%", percent))

	status := types.EvaluateGeThreshold(percent, float64(ins.UsedMemoryPct.WarnGe), float64(ins.UsedMemoryPct.CriticalGe))
	switch status {
	case types.EventStatusCritical:
		q.PushFront(event.SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("redis used memory %.1f%% >= critical threshold %d%%", percent, ins.UsedMemoryPct.CriticalGe)))
	case types.EventStatusWarning:
		q.PushFront(event.SetEventStatus(types.EventStatusWarning).
			SetDescription(fmt.Sprintf("redis used memory %.1f%% >= warning threshold %d%%", percent, ins.UsedMemoryPct.WarnGe)))
	default:
		q.PushFront(event.SetDescription(fmt.Sprintf("redis used memory %.1f%% of maxmemory, everything is ok", percent)))
	}
}

func calculateReplicationLag(info map[string]string) (lag int64, description string, attrs map[string]string, err error) {
	role := strings.ToLower(strings.TrimSpace(info["role"]))
	switch role {
	case "slave", "replica":
		masterOffset, ok, err := infoGetInt64(info, "master_repl_offset")
		if err != nil {
			return 0, "", nil, fmt.Errorf("parse master_repl_offset: %w", err)
		}
		if !ok {
			return 0, "", nil, fmt.Errorf("redis replication info missing master_repl_offset")
		}
		slaveOffset, ok, err := infoGetInt64(info, "slave_repl_offset")
		if err != nil {
			return 0, "", nil, fmt.Errorf("parse slave_repl_offset: %w", err)
		}
		if !ok {
			return 0, "", nil, fmt.Errorf("redis replication info missing slave_repl_offset")
		}
		lag = masterOffset - slaveOffset
		if lag < 0 {
			lag = 0
		}
		return lag, fmt.Sprintf("redis replication lag %s, everything is ok", conv.HumanBytes(uint64(lag))), map[string]string{
			"master_repl_offset": strconv.FormatInt(masterOffset, 10),
			"slave_repl_offset":  strconv.FormatInt(slaveOffset, 10),
		}, nil
	case "master":
		masterOffset, ok, err := infoGetInt64(info, "master_repl_offset")
		if err != nil {
			return 0, "", nil, fmt.Errorf("parse master_repl_offset: %w", err)
		}
		if !ok {
			return 0, "", nil, fmt.Errorf("redis replication info missing master_repl_offset")
		}
		connectedSlaves, _, err := infoGetInt(info, "connected_slaves")
		if err != nil {
			return 0, "", nil, fmt.Errorf("parse connected_slaves: %w", err)
		}
		maxLag, replicas, err := maxReplicaLagFromMaster(info, masterOffset)
		if err != nil {
			return 0, "", nil, err
		}
		if replicas == 0 || connectedSlaves == 0 {
			return -1, "redis replication lag skipped: no connected replicas", map[string]string{
				"master_repl_offset": strconv.FormatInt(masterOffset, 10),
				"connected_slaves":   strconv.Itoa(connectedSlaves),
			}, nil
		}
		return maxLag, fmt.Sprintf("redis replication lag max %s across %d replicas, everything is ok", conv.HumanBytes(uint64(maxLag)), replicas), map[string]string{
			"master_repl_offset": strconv.FormatInt(masterOffset, 10),
			"replica_count":      strconv.Itoa(replicas),
		}, nil
	default:
		return 0, "", nil, fmt.Errorf("unsupported redis role %q for replication lag", role)
	}
}

func maxReplicaLagFromMaster(info map[string]string, masterOffset int64) (int64, int, error) {
	var maxLag int64
	var replicas int
	for key, value := range info {
		if !strings.HasPrefix(key, "slave") {
			continue
		}
		fields := strings.Split(value, ",")
		var replicaOffset int64
		var found bool
		for _, field := range fields {
			parts := strings.SplitN(strings.TrimSpace(field), "=", 2)
			if len(parts) != 2 {
				continue
			}
			if parts[0] != "offset" {
				continue
			}
			offset, err := strconv.ParseInt(parts[1], 10, 64)
			if err != nil {
				return 0, 0, fmt.Errorf("parse %s offset: %w", key, err)
			}
			replicaOffset = offset
			found = true
			break
		}
		if !found {
			continue
		}
		replicas++
		lag := masterOffset - replicaOffset
		if lag < 0 {
			lag = 0
		}
		if lag > maxLag {
			maxLag = lag
		}
	}
	return maxLag, replicas, nil
}

func (ins *Instance) checkPersistence(q *safe.Queue[*types.Event], target string, info map[string]string) {
	event := ins.newEvent("redis::persistence", target)

	loading, ok, err := infoGetInt(info, "loading")
	if err != nil {
		q.PushFront(event.SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("failed to parse redis loading state: %v", err)))
		return
	}
	if !ok {
		q.PushFront(event.SetEventStatus(types.EventStatusCritical).
			SetDescription("redis persistence info missing loading"))
		return
	}

	attrs := map[string]string{"loading": strconv.Itoa(loading)}
	if v, ok := info["rdb_last_bgsave_status"]; ok {
		attrs["rdb_last_bgsave_status"] = v
	}
	if v, ok := info["aof_enabled"]; ok {
		attrs["aof_enabled"] = v
	}
	if v, ok := info["aof_last_write_status"]; ok {
		attrs["aof_last_write_status"] = v
	}
	if v, ok := info["rdb_bgsave_in_progress"]; ok {
		attrs["rdb_bgsave_in_progress"] = v
	}
	if v, ok := info["aof_rewrite_in_progress"]; ok {
		attrs["aof_rewrite_in_progress"] = v
	}
	attrs["threshold_desc"] = fmt.Sprintf("%s: persistence not healthy", ins.Persistence.Severity)
	event.SetAttrs(attrs)

	if loading == 1 {
		q.PushFront(event.SetEventStatus(ins.Persistence.Severity).
			SetDescription("redis is loading persistence data"))
		return
	}

	if status, ok := info["rdb_last_bgsave_status"]; ok && status != "" && strings.ToLower(status) != "ok" {
		q.PushFront(event.SetEventStatus(ins.Persistence.Severity).
			SetDescription(fmt.Sprintf("redis RDB last bgsave status is %s", status)))
		return
	}

	if aofEnabled, ok := info["aof_enabled"]; ok && aofEnabled == "1" {
		if status, ok := info["aof_last_write_status"]; ok && status != "" && strings.ToLower(status) != "ok" {
			q.PushFront(event.SetEventStatus(ins.Persistence.Severity).
				SetDescription(fmt.Sprintf("redis AOF last write status is %s", status)))
			return
		}
	}

	q.PushFront(event.SetDescription("redis persistence status is healthy"))
}
