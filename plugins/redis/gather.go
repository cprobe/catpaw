package redis

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/cprobe/catpaw/logger"
	"github.com/cprobe/catpaw/pkg/safe"
	"github.com/cprobe/catpaw/types"
	"github.com/toolkits/pkg/concurrent/semaphore"
)

func (ins *Instance) Gather(q *safe.Queue[*types.Event]) {
	if len(ins.Targets) == 0 {
		return
	}

	perTarget := time.Duration(ins.Timeout) + time.Duration(ins.ReadTimeout)*8
	batches := (len(ins.Targets) + ins.Concurrency - 1) / ins.Concurrency
	gatherTimeout := perTarget * time.Duration(batches+1)
	if gatherTimeout < 30*time.Second {
		gatherTimeout = 30 * time.Second
	}

	wg := new(sync.WaitGroup)
	se := semaphore.NewSemaphore(ins.Concurrency)
	for _, target := range ins.Targets {
		if startTime, ok := ins.inFlight.Load(target); ok {
			elapsed := time.Now().Unix() - startTime.(int64)
			if elapsed > int64(gatherTimeout.Seconds()) {
				q.PushFront(ins.buildHungEvent(target, elapsed))
			}
			continue
		}

		if _, wasHung := ins.prevHung.Load(target); wasHung {
			q.PushFront(ins.buildHungRecoveryEvent(target))
			ins.prevHung.Delete(target)
		}

		wg.Add(1)
		go func(target string) {
			se.Acquire()
			defer func() {
				if r := recover(); r != nil {
					logger.Logger.Errorw("panic in redis gather goroutine", "target", target, "recover", r)
					q.PushFront(types.BuildEvent(map[string]string{
						"check":  "redis::connectivity",
						"target": target,
					}).SetEventStatus(types.EventStatusCritical).
						SetDescription(fmt.Sprintf("panic during check: %v", r)))
				}
				ins.inFlight.Delete(target)
				se.Release()
				wg.Done()
			}()
			ins.inFlight.Store(target, time.Now().Unix())
			ins.gatherTarget(q, target)
		}(target)
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(gatherTimeout):
		logger.Logger.Errorw("redis gather timeout, some targets may still be running",
			"timeout", gatherTimeout, "targets", len(ins.Targets))
		ins.inFlight.Range(func(key, value any) bool {
			ins.prevHung.Store(key, true)
			return true
		})
	}
}

func (ins *Instance) newAccessor(target string) (*RedisAccessor, error) {
	return NewRedisAccessor(RedisAccessorConfig{
		Target:      target,
		Username:    ins.Username,
		Password:    ins.Password,
		DB:          ins.DB,
		Timeout:     time.Duration(ins.Timeout),
		ReadTimeout: time.Duration(ins.ReadTimeout),
		TLSConfig:   ins.tlsConfig,
		DialFunc:    ins.dialFunc,
	})
}

func (ins *Instance) gatherTarget(q *safe.Queue[*types.Event], target string) {
	connEvent := ins.newEvent("redis::connectivity", target)
	start := time.Now()

	acc, err := ins.newAccessor(target)
	if err != nil {
		connEvent.SetAttrs(map[string]string{
			"response_time":  time.Since(start).String(),
			"threshold_desc": fmt.Sprintf("%s: redis ping failed", ins.Connectivity.Severity),
		})
		q.PushFront(connEvent.SetEventStatus(ins.Connectivity.Severity).
			SetDescription(fmt.Sprintf("redis ping failed: %v", err)))
		return
	}
	defer acc.Close()

	if err := acc.Ping(); err != nil {
		connEvent.SetAttrs(map[string]string{
			"response_time":  time.Since(start).String(),
			"threshold_desc": fmt.Sprintf("%s: redis ping failed", ins.Connectivity.Severity),
		})
		q.PushFront(connEvent.SetEventStatus(ins.Connectivity.Severity).
			SetDescription(fmt.Sprintf("redis ping failed: %v", err)))
		return
	}

	responseTime := time.Since(start)
	connEvent.SetAttrs(map[string]string{
		"response_time":  responseTime.String(),
		"threshold_desc": fmt.Sprintf("%s: redis ping failed", ins.Connectivity.Severity),
	})
	q.PushFront(connEvent.SetDescription("redis ping ok"))

	ins.checkResponseTime(q, target, responseTime)

	infoCache := make(map[string]map[string]string)
	infoSection := func(section string) (map[string]string, error) {
		if info, ok := infoCache[section]; ok {
			return info, nil
		}
		info, err := acc.Info(section)
		if err != nil {
			return nil, err
		}
		infoCache[section] = info
		return info, nil
	}

	if ins.Mode != redisModeStandalone {
		serverInfo, err := infoSection("server")
		if err != nil {
			if ins.clusterStateEnabled() {
				q.PushFront(ins.newEvent("redis::cluster_state", target).
					SetEventStatus(types.EventStatusCritical).
					SetDescription(fmt.Sprintf("failed to query redis server info: %v", err)))
			}
			if ins.clusterTopologyEnabled() {
				q.PushFront(ins.newEvent("redis::cluster_topology", target).
					SetEventStatus(types.EventStatusCritical).
					SetDescription(fmt.Sprintf("failed to query redis server info: %v", err)))
			}
		} else {
			actualMode := strings.ToLower(strings.TrimSpace(serverInfo["redis_mode"]))
			if actualMode == "" {
				actualMode = redisModeStandalone
			}
			if ins.Mode == redisModeCluster && actualMode != redisModeCluster {
				if ins.clusterStateEnabled() {
					q.PushFront(ins.newEvent("redis::cluster_state", target).
						SetEventStatus(types.EventStatusCritical).
						SetAttrs(map[string]string{
							"redis_mode":     actualMode,
							"threshold_desc": fmt.Sprintf("%s: redis_mode must be cluster", ins.ClusterState.Severity),
						}).
						SetDescription(fmt.Sprintf("redis mode is %s, expected cluster", actualMode)))
				}
				if ins.clusterTopologyEnabled() {
					q.PushFront(ins.newEvent("redis::cluster_topology", target).
						SetEventStatus(types.EventStatusCritical).
						SetAttrs(map[string]string{
							"redis_mode":     actualMode,
							"threshold_desc": "Critical: redis_mode must be cluster",
						}).
						SetDescription(fmt.Sprintf("redis mode is %s, expected cluster", actualMode)))
				}
			} else if actualMode == redisModeCluster {
				clusterInfoRaw, clusterInfoErr := acc.ClusterInfo()
				clusterInfo := parseInfoToMap(clusterInfoRaw)
				if ins.clusterStateEnabled() {
					ins.checkClusterState(q, target, clusterInfo, clusterInfoErr)
				}
				if ins.clusterTopologyEnabled() {
					nodesRaw, nodesErr := acc.ClusterNodes()
					ins.checkClusterTopology(q, target, clusterInfo, clusterInfoErr, nodesRaw, nodesErr)
				}
			}
		}
	}

	if ins.Role.Expect != "" {
		info, err := infoSection("replication")
		if err != nil {
			q.PushFront(ins.newEvent("redis::role", target).
				SetEventStatus(types.EventStatusCritical).
				SetDescription(fmt.Sprintf("failed to query redis replication info: %v", err)))
		} else {
			ins.checkRole(q, target, info)
		}
	}

	if ins.MasterLink.Expect != "" {
		info, err := infoSection("replication")
		if err != nil {
			q.PushFront(ins.newEvent("redis::master_link_status", target).
				SetEventStatus(types.EventStatusCritical).
				SetDescription(fmt.Sprintf("failed to query redis replication info: %v", err)))
		} else {
			ins.checkMasterLink(q, target, info)
		}
	}

	if ins.ConnectedSlaves.WarnLt > 0 || ins.ConnectedSlaves.CriticalLt > 0 {
		info, err := infoSection("replication")
		if err != nil {
			q.PushFront(ins.newEvent("redis::connected_slaves", target).
				SetEventStatus(types.EventStatusCritical).
				SetDescription(fmt.Sprintf("failed to query redis replication info: %v", err)))
		} else {
			ins.checkMinCountFromInfo(q, target, "redis::connected_slaves", info, "connected_slaves",
				ins.ConnectedSlaves, "connected slaves")
		}
	}

	if ins.ReplLag.WarnGe > 0 || ins.ReplLag.CriticalGe > 0 {
		info, err := infoSection("replication")
		if err != nil {
			q.PushFront(ins.newEvent("redis::repl_lag", target).
				SetEventStatus(types.EventStatusCritical).
				SetDescription(fmt.Sprintf("failed to query redis replication info: %v", err)))
		} else {
			ins.checkReplLag(q, target, info)
		}
	}

	if ins.ConnectedClients.WarnGe > 0 || ins.ConnectedClients.CriticalGe > 0 || ins.BlockedClients.WarnGe > 0 || ins.BlockedClients.CriticalGe > 0 {
		info, err := infoSection("clients")
		if err != nil {
			if ins.ConnectedClients.WarnGe > 0 || ins.ConnectedClients.CriticalGe > 0 {
				q.PushFront(ins.newEvent("redis::connected_clients", target).
					SetEventStatus(types.EventStatusCritical).
					SetDescription(fmt.Sprintf("failed to query redis client info: %v", err)))
			}
			if ins.BlockedClients.WarnGe > 0 || ins.BlockedClients.CriticalGe > 0 {
				q.PushFront(ins.newEvent("redis::blocked_clients", target).
					SetEventStatus(types.EventStatusCritical).
					SetDescription(fmt.Sprintf("failed to query redis client info: %v", err)))
			}
		} else {
			if ins.ConnectedClients.WarnGe > 0 || ins.ConnectedClients.CriticalGe > 0 {
				ins.checkCountFromInfo(q, target, "redis::connected_clients", info, "connected_clients",
					ins.ConnectedClients, "connected clients")
			}
			if ins.BlockedClients.WarnGe > 0 || ins.BlockedClients.CriticalGe > 0 {
				ins.checkCountFromInfo(q, target, "redis::blocked_clients", info, "blocked_clients",
					ins.BlockedClients, "blocked clients")
			}
		}
	}

	if ins.UsedMemory.WarnGe > 0 || ins.UsedMemory.CriticalGe > 0 {
		info, err := infoSection("memory")
		if err != nil {
			q.PushFront(ins.newEvent("redis::used_memory", target).
				SetEventStatus(types.EventStatusCritical).
				SetDescription(fmt.Sprintf("failed to query redis memory info: %v", err)))
		} else {
			ins.checkUsedMemory(q, target, info)
		}
	} else if ins.UsedMemoryPct.WarnGe > 0 || ins.UsedMemoryPct.CriticalGe > 0 {
		info, err := infoSection("memory")
		if err != nil {
			q.PushFront(ins.newEvent("redis::used_memory_pct", target).
				SetEventStatus(types.EventStatusCritical).
				SetDescription(fmt.Sprintf("failed to query redis memory info: %v", err)))
		} else {
			ins.checkUsedMemoryPct(q, target, info)
		}
	}
	if (ins.UsedMemory.WarnGe > 0 || ins.UsedMemory.CriticalGe > 0) && (ins.UsedMemoryPct.WarnGe > 0 || ins.UsedMemoryPct.CriticalGe > 0) {
		info, err := infoSection("memory")
		if err != nil {
			q.PushFront(ins.newEvent("redis::used_memory_pct", target).
				SetEventStatus(types.EventStatusCritical).
				SetDescription(fmt.Sprintf("failed to query redis memory info: %v", err)))
		} else {
			ins.checkUsedMemoryPct(q, target, info)
		}
	}

	needStats := (ins.RejectedConn.WarnGe > 0 || ins.RejectedConn.CriticalGe > 0) ||
		(ins.EvictedKeys.WarnGe > 0 || ins.EvictedKeys.CriticalGe > 0) ||
		(ins.ExpiredKeys.WarnGe > 0 || ins.ExpiredKeys.CriticalGe > 0) ||
		(ins.OpsPerSecond.WarnGe > 0 || ins.OpsPerSecond.CriticalGe > 0)
	if needStats {
		info, err := infoSection("stats")
		if err != nil {
			if ins.RejectedConn.WarnGe > 0 || ins.RejectedConn.CriticalGe > 0 {
				q.PushFront(ins.newEvent("redis::rejected_connections", target).
					SetEventStatus(types.EventStatusCritical).
					SetDescription(fmt.Sprintf("failed to query redis stats info: %v", err)))
			}
			if ins.EvictedKeys.WarnGe > 0 || ins.EvictedKeys.CriticalGe > 0 {
				q.PushFront(ins.newEvent("redis::evicted_keys", target).
					SetEventStatus(types.EventStatusCritical).
					SetDescription(fmt.Sprintf("failed to query redis stats info: %v", err)))
			}
			if ins.ExpiredKeys.WarnGe > 0 || ins.ExpiredKeys.CriticalGe > 0 {
				q.PushFront(ins.newEvent("redis::expired_keys", target).
					SetEventStatus(types.EventStatusCritical).
					SetDescription(fmt.Sprintf("failed to query redis stats info: %v", err)))
			}
			if ins.OpsPerSecond.WarnGe > 0 || ins.OpsPerSecond.CriticalGe > 0 {
				q.PushFront(ins.newEvent("redis::instantaneous_ops_per_sec", target).
					SetEventStatus(types.EventStatusCritical).
					SetDescription(fmt.Sprintf("failed to query redis stats info: %v", err)))
			}
		} else {
			ins.checkCounterDeltas(q, target, info)
			if ins.OpsPerSecond.WarnGe > 0 || ins.OpsPerSecond.CriticalGe > 0 {
				ins.checkCountFromInfo(q, target, "redis::instantaneous_ops_per_sec", info, "instantaneous_ops_per_sec",
					CountCheck{
						WarnGe:     ins.OpsPerSecond.WarnGe,
						CriticalGe: ins.OpsPerSecond.CriticalGe,
					},
					"instantaneous ops per second")
			}
		}
	}

	if ins.Persistence.Enabled {
		info, err := infoSection("persistence")
		if err != nil {
			q.PushFront(ins.newEvent("redis::persistence", target).
				SetEventStatus(types.EventStatusCritical).
				SetDescription(fmt.Sprintf("failed to query redis persistence info: %v", err)))
		} else {
			ins.checkPersistence(q, target, info)
		}
	}
}

func (ins *Instance) newEvent(check, target string) *types.Event {
	labels := map[string]string{
		"check":  check,
		"target": target,
	}
	if ins.ClusterName != "" {
		labels["cluster_name"] = ins.ClusterName
	}
	return types.BuildEvent(labels)
}

func (ins *Instance) buildHungEvent(target string, elapsedSec int64) *types.Event {
	return types.BuildEvent(map[string]string{
		"check":  "redis::hung",
		"target": target,
	}).SetAttrs(map[string]string{
		"elapsed_seconds": fmt.Sprintf("%d", elapsedSec),
		"threshold_desc":  "Critical: redis check hung",
	}).SetEventStatus(types.EventStatusCritical).
		SetDescription(fmt.Sprintf("redis check hung for %d seconds (target may be unreachable or blocked)", elapsedSec))
}

func (ins *Instance) buildHungRecoveryEvent(target string) *types.Event {
	return types.BuildEvent(map[string]string{
		"check":  "redis::hung",
		"target": target,
	}).SetDescription("redis check recovered from hung state")
}
