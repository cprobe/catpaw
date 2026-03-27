package etcd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cprobe/catpaw/digcore/config"
	"github.com/cprobe/catpaw/digcore/logger"
	"github.com/cprobe/catpaw/digcore/pkg/safe"
	"github.com/cprobe/catpaw/digcore/plugins"
	"github.com/cprobe/catpaw/digcore/types"
	"github.com/toolkits/pkg/concurrent/semaphore"
)

const (
	pluginName      = "etcd"
	maxMetricsBytes = 8 << 20
	maxBodyReadSize = 1 << 20

	defaultQuotaBackendBytes = 2 * 1024 * 1024 * 1024 // 2GB — etcd default
)

// ---------------------------------------------------------------------------
// Check config types (following Redis plugin conventions)
// ---------------------------------------------------------------------------

type QuantileCheck struct {
	Phi        float64         `toml:"phi"`
	WarnGe     config.Duration `toml:"warn_ge"`
	CriticalGe config.Duration `toml:"critical_ge"`
	Disabled   bool            `toml:"disabled"`
}

type HasLeaderCheck struct {
	Disabled bool   `toml:"disabled"`
	Severity string `toml:"severity"`
}

type AlarmCheck struct {
	Disabled bool   `toml:"disabled"`
	Severity string `toml:"severity"`
}

type DbSizePctCheck struct {
	Disabled          bool        `toml:"disabled"`
	QuotaBackendBytes config.Size `toml:"quota_backend_bytes"`
	WarnGe            float64     `toml:"warn_ge"`
	CriticalGe        float64     `toml:"critical_ge"`
}

type DeltaCounterCheck struct {
	WarnGe     int `toml:"warn_ge"`
	CriticalGe int `toml:"critical_ge"`
}

// ---------------------------------------------------------------------------
// Instance & Plugin
// ---------------------------------------------------------------------------

type Instance struct {
	config.InternalConfig

	Targets              []string        `toml:"targets"`
	Concurrency          int             `toml:"concurrency"`
	HealthPath           string          `toml:"health_path"`
	MetricsPath          string          `toml:"metrics_path"`
	HistogramMetric      string          `toml:"histogram_metric"`
	ConnectivitySeverity string          `toml:"connectivity_severity"`
	QuantileChecks       []QuantileCheck `toml:"quantile_checks"`

	// Layer 2: cluster role — default ON
	HasLeader HasLeaderCheck `toml:"has_leader"`

	// Layer 3: capacity — default ON (80/90, quota=2GB)
	DbSizePct DbSizePctCheck `toml:"db_size_pct"`

	// Layer 4: alarms — default ON
	Alarm AlarmCheck `toml:"alarm"`

	// WAL fsync latency — same histogram approach as backend commit
	WalFsyncMetric string          `toml:"wal_fsync_metric"`
	WalFsyncChecks []QuantileCheck `toml:"wal_fsync_checks"`

	// Delta counters — configurable, default OFF
	SlowApply       DeltaCounterCheck `toml:"slow_apply"`
	LeaderChanges   DeltaCounterCheck `toml:"leader_changes"`
	ProposalsFailed DeltaCounterCheck `toml:"proposals_failed"`

	config.HTTPConfig
	client *http.Client

	statsMu     sync.Mutex
	prevStats   map[string]etcdCounterSnapshot
	initialized map[string]bool

	cachedQuota map[string]int64
}

type etcdCounterSnapshot struct {
	slowApply       uint64
	leaderChanges   uint64
	proposalsFailed uint64
}

type EtcdPlugin struct {
	config.InternalConfig
	Instances []*Instance `toml:"instances"`
}

func init() {
	plugins.Add(pluginName, func() plugins.Plugin {
		return &EtcdPlugin{}
	})
}

func (p *EtcdPlugin) GetInstances() []plugins.Instance {
	ret := make([]plugins.Instance, len(p.Instances))
	for i := range p.Instances {
		ret[i] = p.Instances[i]
	}
	return ret
}

func (ins *Instance) Init() error {
	if len(ins.Targets) == 0 {
		return nil
	}

	if ins.HealthPath == "" {
		ins.HealthPath = "/health"
	}
	if ins.MetricsPath == "" {
		ins.MetricsPath = "/metrics"
	}
	if ins.HistogramMetric == "" {
		ins.HistogramMetric = "etcd_disk_backend_commit_duration_seconds"
	}
	if ins.WalFsyncMetric == "" {
		ins.WalFsyncMetric = "etcd_disk_wal_fsync_duration_seconds"
	}
	if ins.Concurrency == 0 {
		ins.Concurrency = 5
	}
	if ins.ConnectivitySeverity == "" {
		ins.ConnectivitySeverity = types.EventStatusCritical
	}

	// HasLeader defaults: enabled, Critical
	if ins.HasLeader.Severity == "" {
		ins.HasLeader.Severity = types.EventStatusCritical
	}

	// Alarm defaults: enabled, Critical
	if ins.Alarm.Severity == "" {
		ins.Alarm.Severity = types.EventStatusCritical
	}

	// DbSizePct defaults: enabled, warn=80%, critical=90%
	// QuotaBackendBytes == 0 means auto-detect from etcd_server_quota_backend_bytes metric
	if ins.DbSizePct.WarnGe == 0 && ins.DbSizePct.CriticalGe == 0 {
		ins.DbSizePct.WarnGe = 80
		ins.DbSizePct.CriticalGe = 90
	}

	for _, target := range ins.Targets {
		if strings.HasPrefix(target, "https://") && (ins.UseTLS == nil || !*ins.UseTLS) {
			t := true
			ins.UseTLS = &t
		}
	}

	if err := validateQuantileChecks("quantile_checks", ins.QuantileChecks); err != nil {
		return err
	}
	if err := validateQuantileChecks("wal_fsync_checks", ins.WalFsyncChecks); err != nil {
		return err
	}
	if err := validateDeltaCounterCheck("slow_apply", ins.SlowApply); err != nil {
		return err
	}
	if err := validateDeltaCounterCheck("leader_changes", ins.LeaderChanges); err != nil {
		return err
	}
	if err := validateDeltaCounterCheck("proposals_failed", ins.ProposalsFailed); err != nil {
		return err
	}
	if ins.DbSizePct.WarnGe > 0 && ins.DbSizePct.CriticalGe > 0 && ins.DbSizePct.WarnGe >= ins.DbSizePct.CriticalGe {
		return fmt.Errorf("db_size_pct.warn_ge(%.1f) must be less than db_size_pct.critical_ge(%.1f)",
			ins.DbSizePct.WarnGe, ins.DbSizePct.CriticalGe)
	}

	client, err := ins.createHTTPClient()
	if err != nil {
		return fmt.Errorf("failed to create http client: %v", err)
	}
	ins.client = client

	if ins.prevStats == nil {
		ins.prevStats = make(map[string]etcdCounterSnapshot)
	}
	if ins.initialized == nil {
		ins.initialized = make(map[string]bool)
	}
	if ins.cachedQuota == nil {
		ins.cachedQuota = make(map[string]int64)
	}

	return nil
}

func validateQuantileChecks(label string, checks []QuantileCheck) error {
	for i, qc := range checks {
		if qc.Disabled {
			continue
		}
		if qc.Phi <= 0 || qc.Phi >= 1 {
			return fmt.Errorf("%s[%d].phi must be in (0,1), got %v", label, i, qc.Phi)
		}
		if qc.WarnGe > 0 && qc.CriticalGe > 0 && qc.WarnGe >= qc.CriticalGe {
			return fmt.Errorf("%s[%d]: warn_ge(%s) must be less than critical_ge(%s)",
				label, i, time.Duration(qc.WarnGe), time.Duration(qc.CriticalGe))
		}
	}
	return nil
}

func validateDeltaCounterCheck(name string, check DeltaCounterCheck) error {
	if check.WarnGe < 0 || check.CriticalGe < 0 {
		return fmt.Errorf("%s thresholds must be >= 0", name)
	}
	if check.WarnGe > 0 && check.CriticalGe > 0 && check.WarnGe >= check.CriticalGe {
		return fmt.Errorf("%s.warn_ge(%d) must be less than %s.critical_ge(%d)",
			name, check.WarnGe, name, check.CriticalGe)
	}
	return nil
}

func (ins *Instance) createHTTPClient() (*http.Client, error) {
	tlsCfg, err := ins.ClientConfig.TLSConfig()
	if err != nil {
		return nil, err
	}

	trans := &http.Transport{
		DialContext:       (&net.Dialer{}).DialContext,
		DisableKeepAlives: true,
		TLSClientConfig:   tlsCfg,
	}

	return &http.Client{
		Transport: trans,
		Timeout:   time.Duration(ins.GetTimeout()),
	}, nil
}

// ---------------------------------------------------------------------------
// Gather — the 4-layer periodic check
// ---------------------------------------------------------------------------

func (ins *Instance) Gather(q *safe.Queue[*types.Event]) {
	if len(ins.Targets) == 0 {
		return
	}

	wg := new(sync.WaitGroup)
	se := semaphore.NewSemaphore(ins.Concurrency)
	for _, target := range ins.Targets {
		wg.Add(1)
		go func(target string) {
			se.Acquire()
			defer func() {
				if r := recover(); r != nil {
					logger.Logger.Errorw("panic in etcd gather goroutine", "target", target, "recover", r)
					q.PushFront(ins.newEvent("etcd::health", target).
						SetEventStatus(types.EventStatusCritical).
						SetDescription(fmt.Sprintf("panic during check: %v", r)))
				}
				se.Release()
				wg.Done()
			}()
			ins.gather(q, target)
		}(target)
	}
	wg.Wait()
}

func (ins *Instance) gather(q *safe.Queue[*types.Event], target string) {
	// Layer 1: connectivity
	ins.gatherHealth(q, target)

	// Pre-fetch /metrics once: reused for L3 quota auto-detection and L5 performance checks
	var metricsBody []byte
	if ins.needMetricsFetch() {
		metricsBody = ins.fetchMetricsBody(target)
	}

	// Layer 2+3: cluster role (has_leader) + capacity (db_size_pct)
	ins.gatherStatus(q, target, metricsBody)

	// Layer 4: alarms (NOSPACE / CORRUPT)
	ins.gatherAlarms(q, target)

	// Layer 5: performance — histograms + delta counters (reuse pre-fetched body)
	ins.processMetrics(q, target, metricsBody)
}

// ---------------------------------------------------------------------------
// Layer 1: health (connectivity)
// ---------------------------------------------------------------------------

func (ins *Instance) gatherHealth(q *safe.Queue[*types.Event], target string) {
	url := target + ins.HealthPath

	healthEvent := ins.newEvent("etcd::health", target)

	resp, err := ins.client.Get(url)
	if err != nil {
		healthEvent.SetEventStatus(ins.ConnectivitySeverity)
		healthEvent.SetDescription(fmt.Sprintf("GET %s failed: %v", ins.HealthPath, err))
		healthEvent.SetAttrs(map[string]string{
			"threshold_desc": fmt.Sprintf("%s: connection failed", ins.ConnectivitySeverity),
		})
		q.PushFront(healthEvent)
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyReadSize))

	if resp.StatusCode != http.StatusOK {
		healthEvent.SetEventStatus(ins.ConnectivitySeverity)
		healthEvent.SetDescription(fmt.Sprintf("GET %s returned HTTP %d: %s", ins.HealthPath, resp.StatusCode, truncate(string(body), 200)))
		healthEvent.SetAttrs(map[string]string{
			"threshold_desc": fmt.Sprintf("%s: non-200 status", ins.ConnectivitySeverity),
			"current_value":  fmt.Sprintf("HTTP %d", resp.StatusCode),
		})
		q.PushFront(healthEvent)
		return
	}

	var result struct {
		Health string `json:"health"`
	}
	if err := json.Unmarshal(body, &result); err != nil || result.Health != "true" {
		healthEvent.SetEventStatus(ins.ConnectivitySeverity)
		desc := fmt.Sprintf("etcd reports unhealthy: %s", truncate(string(body), 200))
		if err != nil {
			desc = fmt.Sprintf("failed to parse health response: %v, body: %s", err, truncate(string(body), 200))
		}
		healthEvent.SetDescription(desc)
		healthEvent.SetAttrs(map[string]string{
			"threshold_desc": fmt.Sprintf("%s: health != true", ins.ConnectivitySeverity),
			"current_value":  result.Health,
		})
		q.PushFront(healthEvent)
		return
	}

	healthEvent.SetDescription("etcd is healthy")
	q.PushFront(healthEvent)
}

// ---------------------------------------------------------------------------
// Metrics pre-fetch & quota auto-detection
// ---------------------------------------------------------------------------

func (ins *Instance) needMetricsFetch() bool {
	if !ins.DbSizePct.Disabled && ins.DbSizePct.QuotaBackendBytes == 0 {
		return true
	}
	return len(activeQuantileChecks(ins.QuantileChecks)) > 0 ||
		len(activeQuantileChecks(ins.WalFsyncChecks)) > 0 ||
		ins.needDeltaCounters()
}

func (ins *Instance) fetchMetricsBody(target string) []byte {
	url := target + ins.MetricsPath
	resp, err := ins.client.Get(url)
	if err != nil {
		logger.Logger.Warnw("etcd: failed to fetch metrics", "target", target, "error", err)
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		logger.Logger.Warnw("etcd: metrics returned non-200",
			"target", target, "status", resp.StatusCode, "body", truncate(string(errBody), 200))
		return nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxMetricsBytes)))
	if err != nil {
		logger.Logger.Warnw("etcd: failed to read metrics body", "target", target, "error", err)
		return nil
	}
	return body
}

// resolveQuota determines the effective quota for db_size_pct checks.
// Priority: user-configured value > cached auto-detect > fresh auto-detect > etcd default (2GB).
func (ins *Instance) resolveQuota(target string, metricsBody []byte) int64 {
	if ins.DbSizePct.QuotaBackendBytes > 0 {
		return int64(ins.DbSizePct.QuotaBackendBytes)
	}

	if len(metricsBody) > 0 {
		for _, line := range strings.Split(string(metricsBody), "\n") {
			if strings.HasPrefix(line, "etcd_server_quota_backend_bytes") {
				if v := parseMetricValue(line); v > 0 {
					quota := int64(v)
					if prev, ok := ins.cachedQuota[target]; !ok || prev != quota {
						logger.Logger.Infow("etcd: auto-detected quota from metrics",
							"target", target, "quota_bytes", quota, "quota_human", humanBytes(v))
					}
					ins.cachedQuota[target] = quota
					return quota
				}
			}
		}
	}

	if cached, ok := ins.cachedQuota[target]; ok && cached > 0 {
		return cached
	}

	logger.Logger.Warnw("etcd: could not auto-detect quota, using etcd default 2GB",
		"target", target)
	ins.cachedQuota[target] = defaultQuotaBackendBytes
	return defaultQuotaBackendBytes
}

// ---------------------------------------------------------------------------
// Layer 2+3: status → has_leader + db_size_pct
// ---------------------------------------------------------------------------

func (ins *Instance) gatherStatus(q *safe.Queue[*types.Event], target string, metricsBody []byte) {
	needLeader := !ins.HasLeader.Disabled
	needDbSize := !ins.DbSizePct.Disabled
	if !needLeader && !needDbSize {
		return
	}

	body, err := ins.postJSON(target, "/v3/maintenance/status")
	if err != nil {
		if needLeader {
			q.PushFront(ins.newEvent("etcd::has_leader", target).
				SetEventStatus(ins.HasLeader.Severity).
				SetDescription(fmt.Sprintf("failed to query maintenance status: %v", err)).
				SetAttrs(map[string]string{
					"threshold_desc": fmt.Sprintf("%s: status query failed", ins.HasLeader.Severity),
				}))
		}
		if needDbSize {
			q.PushFront(ins.newEvent("etcd::db_size_pct", target).
				SetEventStatus(types.EventStatusWarning).
				SetDescription(fmt.Sprintf("failed to query maintenance status: %v", err)).
				SetAttrs(map[string]string{
					"threshold_desc": "Warning: status query failed",
				}))
		}
		return
	}

	// etcd gRPC gateway serializes protobuf int64 fields as JSON strings.
	var status struct {
		Leader   string `json:"leader"`
		DbSize   string `json:"dbSize"`
		RaftTerm string `json:"raftTerm"`
	}
	if err := json.Unmarshal(body, &status); err != nil {
		logger.Logger.Warnw("etcd: failed to parse status response", "target", target, "error", err)
		return
	}

	if needLeader {
		ins.checkHasLeader(q, target, status.Leader)
	}
	if needDbSize {
		dbSize, _ := strconv.ParseFloat(status.DbSize, 64)
		quota := ins.resolveQuota(target, metricsBody)
		ins.checkDbSizePct(q, target, dbSize, quota)
	}
}

func (ins *Instance) checkHasLeader(q *safe.Queue[*types.Event], target, leader string) {
	event := ins.newEvent("etcd::has_leader", target)
	event.SetAttrs(map[string]string{
		"leader":         leader,
		"threshold_desc": fmt.Sprintf("%s: cluster has no leader", ins.HasLeader.Severity),
	}).SetCurrentValue(leader)

	if leader == "" || leader == "0" {
		event.SetEventStatus(ins.HasLeader.Severity)
		event.SetDescription("etcd cluster has no leader — cluster is unavailable for writes")
		q.PushFront(event)
		return
	}

	event.SetDescription(fmt.Sprintf("etcd cluster has leader (ID: %s)", leader))
	q.PushFront(event)
}

func (ins *Instance) checkDbSizePct(q *safe.Queue[*types.Event], target string, dbSize float64, quotaBytes int64) {
	event := ins.newEvent("etcd::db_size_pct", target)

	quota := float64(quotaBytes)
	pct := dbSize / quota * 100

	var parts []string
	if ins.DbSizePct.WarnGe > 0 {
		parts = append(parts, fmt.Sprintf("Warning >= %.0f%%", ins.DbSizePct.WarnGe))
	}
	if ins.DbSizePct.CriticalGe > 0 {
		parts = append(parts, fmt.Sprintf("Critical >= %.0f%%", ins.DbSizePct.CriticalGe))
	}

	pctStr := fmt.Sprintf("%.1f%%", pct)
	event.SetAttrs(map[string]string{
		"current_value":     pctStr,
		"db_size":           humanBytes(dbSize),
		"quota":             humanBytes(quota),
		"threshold_desc":    strings.Join(parts, ", "),
	}).SetCurrentValue(pctStr)

	status := types.EvaluateGeThreshold(pct, ins.DbSizePct.WarnGe, ins.DbSizePct.CriticalGe)
	switch status {
	case types.EventStatusCritical:
		event.SetEventStatus(types.EventStatusCritical)
		event.SetDescription(fmt.Sprintf("etcd db size %s (%.1f%% of quota %s) >= critical threshold %.0f%%",
			humanBytes(dbSize), pct, humanBytes(quota), ins.DbSizePct.CriticalGe))
	case types.EventStatusWarning:
		event.SetEventStatus(types.EventStatusWarning)
		event.SetDescription(fmt.Sprintf("etcd db size %s (%.1f%% of quota %s) >= warning threshold %.0f%%",
			humanBytes(dbSize), pct, humanBytes(quota), ins.DbSizePct.WarnGe))
	default:
		event.SetDescription(fmt.Sprintf("etcd db size %s (%.1f%% of quota %s)",
			humanBytes(dbSize), pct, humanBytes(quota)))
	}
	q.PushFront(event)
}

// ---------------------------------------------------------------------------
// Layer 4: alarms (NOSPACE / CORRUPT)
// ---------------------------------------------------------------------------

func (ins *Instance) gatherAlarms(q *safe.Queue[*types.Event], target string) {
	if ins.Alarm.Disabled {
		return
	}

	event := ins.newEvent("etcd::alarm", target)

	payload := map[string]interface{}{"action": 0, "memberID": 0}
	body, err := ins.postJSONPayload(target, "/v3/maintenance/alarm", payload)
	if err != nil {
		event.SetEventStatus(types.EventStatusWarning)
		event.SetDescription(fmt.Sprintf("failed to query alarms: %v", err))
		event.SetAttrs(map[string]string{
			"threshold_desc": fmt.Sprintf("%s: alarm query failed", ins.Alarm.Severity),
		})
		q.PushFront(event)
		return
	}

	var resp struct {
		Alarms []struct {
			Alarm    string `json:"alarm"`
			MemberID string `json:"memberID"`
		} `json:"alarms"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		event.SetEventStatus(types.EventStatusWarning)
		event.SetDescription(fmt.Sprintf("failed to parse alarm response: %v", err))
		q.PushFront(event)
		return
	}

	event.SetAttrs(map[string]string{
		"alarm_count":    strconv.Itoa(len(resp.Alarms)),
		"threshold_desc": fmt.Sprintf("%s: active alarms detected", ins.Alarm.Severity),
	})

	if len(resp.Alarms) == 0 {
		event.SetDescription("no active etcd alarms")
		q.PushFront(event)
		return
	}

	var alarmDescs []string
	for _, a := range resp.Alarms {
		alarmDescs = append(alarmDescs, fmt.Sprintf("%s(member:%s)", a.Alarm, a.MemberID))
	}
	event.SetEventStatus(ins.Alarm.Severity)
	event.SetCurrentValue(strings.Join(alarmDescs, ", "))
	event.SetDescription(fmt.Sprintf("etcd has %d active alarm(s): %s",
		len(resp.Alarms), strings.Join(alarmDescs, ", ")))
	q.PushFront(event)
}

// ---------------------------------------------------------------------------
// Layer 5: metrics — histograms (backend_commit + wal_fsync) + delta counters
// ---------------------------------------------------------------------------

func (ins *Instance) processMetrics(q *safe.Queue[*types.Event], target string, metricsBody []byte) {
	commitChecks := activeQuantileChecks(ins.QuantileChecks)
	walChecks := activeQuantileChecks(ins.WalFsyncChecks)
	needDeltas := ins.needDeltaCounters()

	if len(commitChecks) == 0 && len(walChecks) == 0 && !needDeltas {
		return
	}

	if metricsBody == nil {
		ins.emitMetricsFetchError(q, target, commitChecks, walChecks,
			fmt.Sprintf("failed to fetch metrics from %s%s", target, ins.MetricsPath))
		return
	}

	// Parse backend commit histogram
	if len(commitChecks) > 0 {
		h, parseErr := parseHistogram(bytes.NewReader(metricsBody), ins.HistogramMetric, maxMetricsBytes)
		if parseErr != nil {
			for _, qc := range commitChecks {
				q.PushFront(ins.newEvent("etcd::backend_commit_"+quantileCheckLabel(qc.Phi), target).
					SetEventStatus(types.EventStatusWarning).
					SetDescription(fmt.Sprintf("failed to parse histogram %s: %v", ins.HistogramMetric, parseErr)).
					SetAttrs(map[string]string{"threshold_desc": "Warning: metrics parse failed"}))
			}
		} else {
			for _, qc := range commitChecks {
				ins.emitQuantileEvent(q, target, "etcd::backend_commit_", qc, h, ins.HistogramMetric)
			}
		}
	}

	// Parse WAL fsync histogram
	if len(walChecks) > 0 {
		h, parseErr := parseHistogram(bytes.NewReader(metricsBody), ins.WalFsyncMetric, maxMetricsBytes)
		if parseErr != nil {
			for _, qc := range walChecks {
				q.PushFront(ins.newEvent("etcd::wal_fsync_"+quantileCheckLabel(qc.Phi), target).
					SetEventStatus(types.EventStatusWarning).
					SetDescription(fmt.Sprintf("failed to parse histogram %s: %v", ins.WalFsyncMetric, parseErr)).
					SetAttrs(map[string]string{"threshold_desc": "Warning: metrics parse failed"}))
			}
		} else {
			for _, qc := range walChecks {
				ins.emitQuantileEvent(q, target, "etcd::wal_fsync_", qc, h, ins.WalFsyncMetric)
			}
		}
	}

	// Parse delta counters
	if needDeltas {
		ins.gatherDeltaCounters(q, target, metricsBody)
	}
}

func (ins *Instance) emitMetricsFetchError(q *safe.Queue[*types.Event], target string, commitChecks, walChecks []QuantileCheck, desc string) {
	for _, qc := range commitChecks {
		q.PushFront(ins.newEvent("etcd::backend_commit_"+quantileCheckLabel(qc.Phi), target).
			SetEventStatus(ins.ConnectivitySeverity).
			SetDescription(desc).
			SetAttrs(map[string]string{"threshold_desc": fmt.Sprintf("%s: metrics fetch failed", ins.ConnectivitySeverity)}))
	}
	for _, qc := range walChecks {
		q.PushFront(ins.newEvent("etcd::wal_fsync_"+quantileCheckLabel(qc.Phi), target).
			SetEventStatus(ins.ConnectivitySeverity).
			SetDescription(desc).
			SetAttrs(map[string]string{"threshold_desc": fmt.Sprintf("%s: metrics fetch failed", ins.ConnectivitySeverity)}))
	}
}

func (ins *Instance) emitQuantileEvent(q *safe.Queue[*types.Event], target, checkPrefix string, qc QuantileCheck, h *histogram, metricName string) {
	checkLabel := checkPrefix + quantileCheckLabel(qc.Phi)
	event := ins.newEvent(checkLabel, target)

	val, err := histogramQuantile(qc.Phi, h)
	if err != nil {
		event.SetEventStatus(types.EventStatusWarning)
		event.SetDescription(fmt.Sprintf("failed to compute quantile phi=%v: %v", qc.Phi, err))
		q.PushFront(event)
		return
	}

	latency := time.Duration(val * float64(time.Second))
	latencyStr := formatLatency(latency)
	warnStr := time.Duration(qc.WarnGe).String()
	critStr := time.Duration(qc.CriticalGe).String()

	var threshParts []string
	if qc.WarnGe > 0 {
		threshParts = append(threshParts, fmt.Sprintf("Warning >= %s", warnStr))
	}
	if qc.CriticalGe > 0 {
		threshParts = append(threshParts, fmt.Sprintf("Critical >= %s", critStr))
	}

	event.SetAttrs(map[string]string{
		"current_value":  latencyStr,
		"threshold_desc": strings.Join(threshParts, ", "),
		"phi":            fmt.Sprintf("%v", qc.Phi),
		"metric":         metricName,
	}).SetCurrentValue(latencyStr)

	label := quantileCheckLabel(qc.Phi)
	shortMetric := strings.TrimPrefix(checkPrefix, "etcd::")
	shortMetric = strings.TrimSuffix(shortMetric, "_")

	if qc.CriticalGe > 0 && latency >= time.Duration(qc.CriticalGe) {
		event.SetEventStatus(types.EventStatusCritical)
		event.SetDescription(fmt.Sprintf("%s %s latency %s >= critical threshold %s",
			shortMetric, label, latencyStr, critStr))
	} else if qc.WarnGe > 0 && latency >= time.Duration(qc.WarnGe) {
		event.SetEventStatus(types.EventStatusWarning)
		event.SetDescription(fmt.Sprintf("%s %s latency %s >= warning threshold %s",
			shortMetric, label, latencyStr, warnStr))
	} else {
		event.SetDescription(fmt.Sprintf("%s %s latency %s, within threshold",
			shortMetric, label, latencyStr))
	}

	q.PushFront(event)
}

// ---------------------------------------------------------------------------
// Delta counters (slow_apply, leader_changes, proposals_failed)
// ---------------------------------------------------------------------------

func (ins *Instance) needDeltaCounters() bool {
	return (ins.SlowApply.WarnGe > 0 || ins.SlowApply.CriticalGe > 0) ||
		(ins.LeaderChanges.WarnGe > 0 || ins.LeaderChanges.CriticalGe > 0) ||
		(ins.ProposalsFailed.WarnGe > 0 || ins.ProposalsFailed.CriticalGe > 0)
}

func (ins *Instance) gatherDeltaCounters(q *safe.Queue[*types.Event], target string, metricsBody []byte) {
	lines := strings.Split(string(metricsBody), "\n")
	gauges := make(map[string]uint64)

	wantMetrics := map[string]bool{
		"etcd_server_slow_apply_total":         true,
		"etcd_server_leader_changes_seen_total": true,
		"etcd_server_proposals_failed_total":    true,
	}

	for _, line := range lines {
		if len(line) == 0 || line[0] == '#' {
			continue
		}
		for name := range wantMetrics {
			if strings.HasPrefix(line, name+" ") || strings.HasPrefix(line, name+"{") {
				val := parseMetricValue(line)
				if val >= 0 {
					gauges[name] = uint64(val)
				}
				break
			}
		}
	}

	var snap etcdCounterSnapshot
	if v, ok := gauges["etcd_server_slow_apply_total"]; ok {
		snap.slowApply = v
	}
	if v, ok := gauges["etcd_server_leader_changes_seen_total"]; ok {
		snap.leaderChanges = v
	}
	if v, ok := gauges["etcd_server_proposals_failed_total"]; ok {
		snap.proposalsFailed = v
	}

	ins.statsMu.Lock()
	prev := ins.prevStats[target]
	wasInitialized := ins.initialized[target]
	ins.prevStats[target] = snap
	ins.initialized[target] = true
	ins.statsMu.Unlock()

	if !wasInitialized {
		if ins.SlowApply.WarnGe > 0 || ins.SlowApply.CriticalGe > 0 {
			q.PushFront(ins.newEvent("etcd::slow_apply", target).SetAttrs(map[string]string{
				"delta": "0", "total": strconv.FormatUint(snap.slowApply, 10),
			}).SetDescription(fmt.Sprintf("etcd slow apply baseline established (total: %d)", snap.slowApply)))
		}
		if ins.LeaderChanges.WarnGe > 0 || ins.LeaderChanges.CriticalGe > 0 {
			q.PushFront(ins.newEvent("etcd::leader_changes", target).SetAttrs(map[string]string{
				"delta": "0", "total": strconv.FormatUint(snap.leaderChanges, 10),
			}).SetDescription(fmt.Sprintf("etcd leader changes baseline established (total: %d)", snap.leaderChanges)))
		}
		if ins.ProposalsFailed.WarnGe > 0 || ins.ProposalsFailed.CriticalGe > 0 {
			q.PushFront(ins.newEvent("etcd::proposals_failed", target).SetAttrs(map[string]string{
				"delta": "0", "total": strconv.FormatUint(snap.proposalsFailed, 10),
			}).SetDescription(fmt.Sprintf("etcd proposals failed baseline established (total: %d)", snap.proposalsFailed)))
		}
		return
	}

	if ins.SlowApply.WarnGe > 0 || ins.SlowApply.CriticalGe > 0 {
		delta := safeDelta(snap.slowApply, prev.slowApply)
		ins.checkDeltaCounter(q, target, "etcd::slow_apply", delta, snap.slowApply, ins.SlowApply, "slow apply")
	}
	if ins.LeaderChanges.WarnGe > 0 || ins.LeaderChanges.CriticalGe > 0 {
		delta := safeDelta(snap.leaderChanges, prev.leaderChanges)
		ins.checkDeltaCounter(q, target, "etcd::leader_changes", delta, snap.leaderChanges, ins.LeaderChanges, "leader changes")
	}
	if ins.ProposalsFailed.WarnGe > 0 || ins.ProposalsFailed.CriticalGe > 0 {
		delta := safeDelta(snap.proposalsFailed, prev.proposalsFailed)
		ins.checkDeltaCounter(q, target, "etcd::proposals_failed", delta, snap.proposalsFailed, ins.ProposalsFailed, "proposals failed")
	}
}

func (ins *Instance) checkDeltaCounter(q *safe.Queue[*types.Event], target, check string, delta, total uint64, thresholds DeltaCounterCheck, metricName string) {
	var parts []string
	if thresholds.WarnGe > 0 {
		parts = append(parts, fmt.Sprintf("Warning >= %d", thresholds.WarnGe))
	}
	if thresholds.CriticalGe > 0 {
		parts = append(parts, fmt.Sprintf("Critical >= %d", thresholds.CriticalGe))
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
		event.SetEventStatus(types.EventStatusCritical)
		event.SetDescription(fmt.Sprintf("etcd %s delta %d >= critical threshold %d", metricName, delta, thresholds.CriticalGe))
	case types.EventStatusWarning:
		event.SetEventStatus(types.EventStatusWarning)
		event.SetDescription(fmt.Sprintf("etcd %s delta %d >= warning threshold %d", metricName, delta, thresholds.WarnGe))
	default:
		event.SetDescription(fmt.Sprintf("etcd %s delta %d, within threshold", metricName, delta))
	}
	q.PushFront(event)
}

func safeDelta(cur, prev uint64) uint64 {
	if cur >= prev {
		return cur - prev
	}
	return 0
}

// ---------------------------------------------------------------------------
// HTTP helpers for gRPC gateway POST calls
// ---------------------------------------------------------------------------

func (ins *Instance) postJSON(target, path string) ([]byte, error) {
	return ins.postJSONPayload(target, path, nil)
}

func (ins *Instance) postJSONPayload(target, path string, payload any) ([]byte, error) {
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

	req, err := http.NewRequest(http.MethodPost, target+path, bodyReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := ins.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyReadSize))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	return body, nil
}

// ---------------------------------------------------------------------------
// Utilities
// ---------------------------------------------------------------------------

func (ins *Instance) newEvent(check, target string) *types.Event {
	return types.BuildEvent(map[string]string{
		"check":  check,
		"target": target,
	})
}

func activeQuantileChecks(checks []QuantileCheck) []QuantileCheck {
	var active []QuantileCheck
	for _, qc := range checks {
		if !qc.Disabled {
			active = append(active, qc)
		}
	}
	return active
}

func formatLatency(d time.Duration) string {
	if d < time.Millisecond {
		return fmt.Sprintf("%.1fus", float64(d)/float64(time.Microsecond))
	}
	if d < time.Second {
		return fmt.Sprintf("%.1fms", float64(d)/float64(time.Millisecond))
	}
	return fmt.Sprintf("%.3fs", d.Seconds())
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "...(truncated)"
}

