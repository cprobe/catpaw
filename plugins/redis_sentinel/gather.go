package redis_sentinel

import (
	"fmt"
	"sort"
	"strconv"
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

	perTarget := time.Duration(ins.Timeout) + time.Duration(ins.ReadTimeout)*10
	batches := (len(ins.Targets) + ins.Concurrency - 1) / ins.Concurrency
	gatherTimeout := perTarget * time.Duration(batches+1)
	minTimeout := time.Duration(defaultGatherMinimum) * time.Second
	if gatherTimeout < minTimeout {
		gatherTimeout = minTimeout
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
					logger.Logger.Errorw("panic in redis_sentinel gather goroutine", "target", target, "recover", r)
					q.PushFront(types.BuildEvent(map[string]string{
						"check":  "redis_sentinel::connectivity",
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
		logger.Logger.Errorw("redis_sentinel gather timeout, some targets may still be running",
			"timeout", gatherTimeout, "targets", len(ins.Targets))
		ins.inFlight.Range(func(key, value any) bool {
			ins.prevHung.Store(key, true)
			return true
		})
	}
}

func (ins *Instance) newAccessor(target string) (*SentinelAccessor, error) {
	return NewSentinelAccessor(SentinelAccessorConfig{
		Target:      target,
		Username:    ins.Username,
		Password:    ins.Password,
		Timeout:     time.Duration(ins.Timeout),
		ReadTimeout: time.Duration(ins.ReadTimeout),
		TLSConfig:   ins.tlsConfig,
		DialFunc:    ins.dialFunc,
	})
}

func (ins *Instance) gatherTarget(q *safe.Queue[*types.Event], target string) {
	start := time.Now()
	acc, err := ins.newAccessor(target)
	if err != nil {
		if severityCheckEnabled(ins.Connectivity, true) {
			q.PushFront(ins.newEvent("redis_sentinel::connectivity", target).
				SetAttrs(map[string]string{
					"response_time":  time.Since(start).String(),
					"threshold_desc": fmt.Sprintf("%s: sentinel ping failed", ins.Connectivity.Severity),
				}).
				SetEventStatus(ins.Connectivity.Severity).
				SetDescription(fmt.Sprintf("sentinel ping failed: %v", err)))
		}
		return
	}
	defer acc.Close()

	if err := acc.Ping(); err != nil {
		if severityCheckEnabled(ins.Connectivity, true) {
			q.PushFront(ins.newEvent("redis_sentinel::connectivity", target).
				SetAttrs(map[string]string{
					"response_time":  time.Since(start).String(),
					"threshold_desc": fmt.Sprintf("%s: sentinel ping failed", ins.Connectivity.Severity),
				}).
				SetEventStatus(ins.Connectivity.Severity).
				SetDescription(fmt.Sprintf("sentinel ping failed: %v", err)))
		}
		return
	}

	if severityCheckEnabled(ins.Connectivity, true) {
		q.PushFront(ins.newEvent("redis_sentinel::connectivity", target).
			SetAttrs(map[string]string{
				"response_time":  time.Since(start).String(),
				"threshold_desc": fmt.Sprintf("%s: sentinel ping failed", ins.Connectivity.Severity),
			}).
			SetDescription("sentinel ping ok"))
	}

	if ins.roleEnabled() {
		role, err := acc.Role()
		if err != nil {
			q.PushFront(ins.newEvent("redis_sentinel::role", target).
				SetEventStatus(types.EventStatusCritical).
				SetDescription(fmt.Sprintf("failed to query sentinel role: %v", err)))
			return
		}
		event := ins.newEvent("redis_sentinel::role", target).
			SetAttrs(map[string]string{
				"actual":         role,
				"expect":         "sentinel",
				"threshold_desc": fmt.Sprintf("%s: role != sentinel", ins.Role.Severity),
			}).
			SetCurrentValue(role)
		if role != "sentinel" {
			q.PushFront(event.SetEventStatus(ins.Role.Severity).
				SetDescription(fmt.Sprintf("sentinel role is %s, expected sentinel", role)))
			return
		}
		q.PushFront(event.SetDescription("sentinel role is sentinel, everything is ok"))
	}

	masters, err := acc.SentinelMasters()
	if err != nil {
		dependencyEvent := ins.newEvent("redis_sentinel::masters_overview", target).
			SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("failed to query SENTINEL MASTERS: %v", err))
		q.PushFront(dependencyEvent)
		return
	}

	discovered := make(map[string]SentinelMasterInfo, len(masters))
	discoveredNames := make([]string, 0, len(masters))
	for _, master := range masters {
		name := strings.TrimSpace(master["name"])
		if name == "" {
			continue
		}
		discovered[name] = master
		discoveredNames = append(discoveredNames, name)
	}
	sort.Strings(discoveredNames)

	if ins.mastersOverviewEnabled() {
		ev := ins.newEvent("redis_sentinel::masters_overview", target).
			SetAttrs(map[string]string{
				"masters_total":      strconv.Itoa(len(discoveredNames)),
				"discovered_masters": strings.Join(discoveredNames, ","),
				"threshold_desc":     fmt.Sprintf("%s: zero monitored masters", ins.MastersOverview.EmptySeverity),
			}).
			SetCurrentValue(strconv.Itoa(len(discoveredNames)))
		if len(ins.Masters) == 0 {
			ev.SetAttrs(map[string]string{
				"per_master_checks": "skipped_no_configured_masters",
			})
		}
		if len(discoveredNames) == 0 {
			q.PushFront(ev.SetEventStatus(ins.MastersOverview.EmptySeverity).
				SetDescription("sentinel is reachable but monitors zero masters"))
		} else {
			if len(ins.Masters) == 0 {
				q.PushFront(ev.SetDescription(fmt.Sprintf("sentinel monitors %d masters; per-master checks are skipped because instances.masters is not configured", len(discoveredNames))))
			} else {
				q.PushFront(ev.SetDescription(fmt.Sprintf("sentinel monitors %d masters, everything is ok", len(discoveredNames))))
			}
		}
	}

	if len(ins.Masters) == 0 {
		return
	}

	for _, masterName := range ins.effectiveMasterNames() {
		masterInfo, ok := discovered[masterName]
		if !ok {
			ins.pushMissingMasterEvents(q, target, masterName)
			continue
		}
		ins.checkMaster(q, acc, target, masterName, masterInfo)
	}

	if ins.tiltEnabled() {
		info, err := acc.Info("")
		if err != nil {
			q.PushFront(ins.newEvent("redis_sentinel::tilt", target).
				SetEventStatus(types.EventStatusCritical).
				SetDescription(fmt.Sprintf("failed to query sentinel INFO: %v", err)))
		} else {
			tilt := strings.TrimSpace(info["sentinel_tilt"])
			ev := ins.newEvent("redis_sentinel::tilt", target).
				SetAttrs(map[string]string{
					"sentinel_tilt":  tilt,
					"threshold_desc": fmt.Sprintf("%s: sentinel_tilt == 1", ins.Tilt.Severity),
				}).
				SetCurrentValue(tilt)
			if tilt == "1" {
				q.PushFront(ev.SetEventStatus(ins.Tilt.Severity).
					SetDescription("sentinel is in tilt mode"))
			} else {
				q.PushFront(ev.SetDescription("sentinel is not in tilt mode"))
			}
		}
	}
}

func (ins *Instance) effectiveMasterNames() []string {
	names := make([]string, 0, len(ins.Masters))
	for _, master := range ins.Masters {
		names = append(names, master.Name)
	}
	return names
}

func (ins *Instance) checkMaster(q *safe.Queue[*types.Event], acc *SentinelAccessor, target, masterName string, master SentinelMasterInfo) {
	flags := master["flags"]

	if ins.ckquorumEnabled() {
		desc, err := acc.SentinelCKQuorum(masterName)
		ev := ins.newMasterEvent("redis_sentinel::ckquorum", target, masterName).
			SetAttrs(map[string]string{
				"threshold_desc": fmt.Sprintf("%s: CKQUORUM must succeed", ins.CKQuorum.Severity),
			})
		if err != nil {
			q.PushFront(ev.SetEventStatus(ins.CKQuorum.Severity).
				SetDescription(fmt.Sprintf("sentinel ckquorum for master %s failed: %v", masterName, err)))
		} else {
			q.PushFront(ev.SetDescription(fmt.Sprintf("sentinel ckquorum for master %s ok: %s", masterName, desc)))
		}
	}

	if ins.masterSDownEnabled() {
		ev := ins.newMasterEvent("redis_sentinel::master_sdown", target, masterName).
			SetAttrs(map[string]string{
				"flags":          flags,
				"threshold_desc": fmt.Sprintf("%s: flags contain s_down", ins.MasterSDown.Severity),
			})
		if hasFlag(flags, "s_down") {
			q.PushFront(ev.SetEventStatus(ins.MasterSDown.Severity).
				SetDescription(fmt.Sprintf("sentinel master %s is subjectively down", masterName)))
		} else {
			q.PushFront(ev.SetDescription(fmt.Sprintf("sentinel master %s is not subjectively down", masterName)))
		}
	}

	if ins.masterODownEnabled() {
		ev := ins.newMasterEvent("redis_sentinel::master_odown", target, masterName).
			SetAttrs(map[string]string{
				"flags":          flags,
				"threshold_desc": fmt.Sprintf("%s: flags contain o_down", ins.MasterODown.Severity),
			})
		if hasFlag(flags, "o_down") {
			q.PushFront(ev.SetEventStatus(ins.MasterODown.Severity).
				SetDescription(fmt.Sprintf("sentinel master %s is objectively down", masterName)))
		} else {
			q.PushFront(ev.SetDescription(fmt.Sprintf("sentinel master %s is not objectively down", masterName)))
		}
	}

	if ins.masterAddrResolutionEnabled() {
		ev := ins.newMasterEvent("redis_sentinel::master_addr_resolution", target, masterName).
			SetAttrs(map[string]string{
				"threshold_desc": fmt.Sprintf("%s: master address must resolve", ins.MasterAddrResolution.Severity),
			})
		addr, err := acc.SentinelGetMasterAddrByName(masterName)
		if err != nil || strings.TrimSpace(addr) == "" {
			desc := fmt.Sprintf("sentinel failed to resolve master %s address", masterName)
			if err != nil {
				desc = fmt.Sprintf("sentinel failed to resolve master %s address: %v", masterName, err)
			}
			q.PushFront(ev.SetEventStatus(ins.MasterAddrResolution.Severity).SetDescription(desc))
		} else {
			q.PushFront(ev.SetAttrs(map[string]string{"resolved_master": addr}).SetCurrentValue(addr).
				SetDescription(fmt.Sprintf("sentinel resolved master %s to %s", masterName, addr)))
		}
	}

	var replicas []SentinelMasterInfo
	var sentinels []SentinelMasterInfo
	replicasLoaded := false
	sentinelsLoaded := false
	var sentinelsErr error
	var replicasErr error

	if ins.peerCountEnabled() || ins.knownSentinelsEnabled() {
		sentinels, sentinelsErr = acc.SentinelSentinels(masterName)
		sentinelsLoaded = true
	}
	if ins.knownReplicasEnabled() {
		replicas, replicasErr = acc.SentinelReplicas(masterName)
		replicasLoaded = true
	}

	if ins.peerCountEnabled() {
		if sentinelsErr != nil {
			q.PushFront(ins.newMasterEvent("redis_sentinel::peer_count", target, masterName).
				SetEventStatus(types.EventStatusCritical).
				SetDescription(fmt.Sprintf("failed to query sentinel peers for master %s: %v", masterName, sentinelsErr)))
		} else {
			ins.checkMinCount(q, target, "redis_sentinel::peer_count", masterName, len(sentinels), ins.PeerCount, "peer sentinels")
		}
	}
	if ins.knownSentinelsEnabled() {
		if sentinelsErr != nil {
			q.PushFront(ins.newMasterEvent("redis_sentinel::known_sentinels", target, masterName).
				SetEventStatus(types.EventStatusCritical).
				SetDescription(fmt.Sprintf("failed to query known sentinels for master %s: %v", masterName, sentinelsErr)))
		} else {
			ins.checkMinCount(q, target, "redis_sentinel::known_sentinels", masterName, len(sentinels), ins.KnownSentinels, "known sentinels")
		}
	}
	if ins.knownReplicasEnabled() {
		if !replicasLoaded {
			replicas, replicasErr = acc.SentinelReplicas(masterName)
			replicasLoaded = true
		}
		if replicasErr != nil {
			q.PushFront(ins.newMasterEvent("redis_sentinel::known_replicas", target, masterName).
				SetEventStatus(types.EventStatusCritical).
				SetDescription(fmt.Sprintf("failed to query known replicas for master %s: %v", masterName, replicasErr)))
		} else {
			ins.checkMinCount(q, target, "redis_sentinel::known_replicas", masterName, len(replicas), ins.KnownReplicas, "known replicas")
		}
	}
	if ins.failoverInProgressEnabled() {
		ev := ins.newMasterEvent("redis_sentinel::failover_in_progress", target, masterName).
			SetAttrs(map[string]string{
				"flags":          flags,
				"threshold_desc": fmt.Sprintf("%s: failover state present", ins.FailoverInProgress.Severity),
			})
		if failoverInProgress(flags) {
			if !sentinelsLoaded {
				sentinels, sentinelsErr = acc.SentinelSentinels(masterName)
				sentinelsLoaded = true
			}
			if sentinelsErr == nil {
				ev.SetAttrs(map[string]string{"known_peers": strconv.Itoa(len(sentinels))})
			}
			q.PushFront(ev.SetEventStatus(ins.FailoverInProgress.Severity).
				SetDescription(fmt.Sprintf("sentinel reports failover in progress for master %s", masterName)))
		} else {
			q.PushFront(ev.SetDescription(fmt.Sprintf("sentinel reports no failover in progress for master %s", masterName)))
		}
	}
}

func (ins *Instance) pushMissingMasterEvents(q *safe.Queue[*types.Event], target, masterName string) {
	desc := fmt.Sprintf("configured master %s is not present in SENTINEL MASTERS", masterName)
	if ins.ckquorumEnabled() {
		q.PushFront(ins.newMasterEvent("redis_sentinel::ckquorum", target, masterName).
			SetEventStatus(ins.CKQuorum.Severity).
			SetDescription(desc))
	}
	if ins.masterSDownEnabled() {
		q.PushFront(ins.newMasterEvent("redis_sentinel::master_sdown", target, masterName).
			SetEventStatus(types.EventStatusCritical).
			SetDescription(desc))
	}
	if ins.masterODownEnabled() {
		q.PushFront(ins.newMasterEvent("redis_sentinel::master_odown", target, masterName).
			SetEventStatus(types.EventStatusCritical).
			SetDescription(desc))
	}
	if ins.masterAddrResolutionEnabled() {
		q.PushFront(ins.newMasterEvent("redis_sentinel::master_addr_resolution", target, masterName).
			SetEventStatus(types.EventStatusCritical).
			SetDescription(desc))
	}
}

func (ins *Instance) checkMinCount(q *safe.Queue[*types.Event], target, check, masterName string, value int, thresholds ThresholdCheck, metricName string) {
	var parts []string
	if thresholds.WarnLt > 0 {
		parts = append(parts, fmt.Sprintf("Warning < %d", thresholds.WarnLt))
	}
	if thresholds.CriticalLt > 0 {
		parts = append(parts, fmt.Sprintf("Critical < %d", thresholds.CriticalLt))
	}
	event := ins.newMasterEvent(check, target, masterName).
		SetAttrs(map[string]string{
			metricName:       strconv.Itoa(value),
			"threshold_desc": strings.Join(parts, ", "),
		}).
		SetCurrentValue(strconv.Itoa(value))

	if thresholds.CriticalLt > 0 && value < thresholds.CriticalLt {
		q.PushFront(event.SetEventStatus(types.EventStatusCritical).
			SetDescription(fmt.Sprintf("sentinel %s %d < critical threshold %d for master %s", metricName, value, thresholds.CriticalLt, masterName)))
		return
	}
	if thresholds.WarnLt > 0 && value < thresholds.WarnLt {
		q.PushFront(event.SetEventStatus(types.EventStatusWarning).
			SetDescription(fmt.Sprintf("sentinel %s %d < warning threshold %d for master %s", metricName, value, thresholds.WarnLt, masterName)))
		return
	}
	q.PushFront(event.SetDescription(fmt.Sprintf("sentinel %s %d for master %s, everything is ok", metricName, value, masterName)))
}

func (ins *Instance) newEvent(check, target string) *types.Event {
	return types.BuildEvent(map[string]string{
		"check":  check,
		"target": target,
	})
}

func (ins *Instance) newMasterEvent(check, target, masterName string) *types.Event {
	return types.BuildEvent(map[string]string{
		"check":       check,
		"target":      target,
		"master_name": masterName,
	})
}

func (ins *Instance) buildHungEvent(target string, elapsedSec int64) *types.Event {
	return types.BuildEvent(map[string]string{
		"check":  "redis_sentinel::hung",
		"target": target,
	}).SetAttrs(map[string]string{
		"elapsed_seconds": fmt.Sprintf("%d", elapsedSec),
		"threshold_desc":  "Critical: sentinel check hung",
	}).SetEventStatus(types.EventStatusCritical).
		SetDescription(fmt.Sprintf("sentinel check hung for %d seconds (target may be unreachable or blocked)", elapsedSec))
}

func (ins *Instance) buildHungRecoveryEvent(target string) *types.Event {
	return types.BuildEvent(map[string]string{
		"check":  "redis_sentinel::hung",
		"target": target,
	}).SetDescription("sentinel check recovered from hung state")
}

func hasFlag(flags, want string) bool {
	for _, token := range strings.Split(strings.ToLower(strings.TrimSpace(flags)), ",") {
		if strings.TrimSpace(token) == want {
			return true
		}
	}
	return false
}

func failoverInProgress(flags string) bool {
	for _, token := range strings.Split(strings.ToLower(strings.TrimSpace(flags)), ",") {
		token = strings.TrimSpace(token)
		if strings.Contains(token, "failover") && token != "no-failover" {
			return true
		}
	}
	return false
}
