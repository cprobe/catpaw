package etcd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cprobe/catpaw/digcore/config"
	"github.com/cprobe/catpaw/digcore/logger"
	"github.com/cprobe/catpaw/digcore/pkg/safe"
	"github.com/cprobe/catpaw/digcore/types"
	"go.uber.org/zap"
)

func TestMain(m *testing.M) {
	l, _ := zap.NewDevelopment()
	logger.Logger = l.Sugar()
	os.Exit(m.Run())
}

// ---------------------------------------------------------------------------
// Init defaults & validation
// ---------------------------------------------------------------------------

func TestInitDefaults(t *testing.T) {
	ins := &Instance{Targets: []string{"http://127.0.0.1:2379"}}
	if err := ins.Init(); err != nil {
		t.Fatal(err)
	}
	if ins.HasLeader.Severity != types.EventStatusCritical {
		t.Errorf("has_leader severity = %q, want Critical", ins.HasLeader.Severity)
	}
	if ins.Alarm.Severity != types.EventStatusCritical {
		t.Errorf("alarm severity = %q, want Critical", ins.Alarm.Severity)
	}
	if ins.DbSizePct.QuotaBackendBytes != 0 {
		t.Errorf("quota = %d, want 0 (auto-detect)", ins.DbSizePct.QuotaBackendBytes)
	}
	if ins.DbSizePct.WarnGe != 80 {
		t.Errorf("db_size_pct.warn_ge = %v, want 80", ins.DbSizePct.WarnGe)
	}
	if ins.DbSizePct.CriticalGe != 90 {
		t.Errorf("db_size_pct.critical_ge = %v, want 90", ins.DbSizePct.CriticalGe)
	}
	if ins.WalFsyncMetric != "etcd_disk_wal_fsync_duration_seconds" {
		t.Errorf("wal_fsync_metric = %q", ins.WalFsyncMetric)
	}
}

func TestInitCustomQuota(t *testing.T) {
	ins := &Instance{
		Targets:   []string{"http://127.0.0.1:2379"},
		DbSizePct: DbSizePctCheck{QuotaBackendBytes: 8 * config.GB},
	}
	if err := ins.Init(); err != nil {
		t.Fatal(err)
	}
	if ins.DbSizePct.QuotaBackendBytes != 8*config.GB {
		t.Errorf("quota = %d, want 8GB", ins.DbSizePct.QuotaBackendBytes)
	}
}

func TestInitValidation(t *testing.T) {
	ins := &Instance{
		Targets:   []string{"http://127.0.0.1:2379"},
		DbSizePct: DbSizePctCheck{WarnGe: 95, CriticalGe: 80},
	}
	err := ins.Init()
	if err == nil {
		t.Fatal("expected validation error for warn_ge >= critical_ge")
	}
	if !strings.Contains(err.Error(), "db_size_pct") {
		t.Errorf("error should mention db_size_pct: %v", err)
	}
}

// ---------------------------------------------------------------------------
// has_leader check
// ---------------------------------------------------------------------------

func popEvent(q *safe.Queue[*types.Event]) *types.Event {
	ptr := q.PopBack()
	if ptr == nil {
		return nil
	}
	return *ptr
}

func TestCheckHasLeader_OK(t *testing.T) {
	ins := &Instance{HasLeader: HasLeaderCheck{Severity: types.EventStatusCritical}}
	q := safe.NewQueue[*types.Event]()
	ins.checkHasLeader(q, "http://test:2379", "12345")
	event := popEvent(q)
	if event.EventStatus != types.EventStatusOk {
		t.Errorf("status = %s, want Ok", event.EventStatus)
	}
	if !strings.Contains(event.Description, "has leader") {
		t.Errorf("desc = %q, should mention has leader", event.Description)
	}
}

func TestCheckHasLeader_NoLeader(t *testing.T) {
	ins := &Instance{HasLeader: HasLeaderCheck{Severity: types.EventStatusCritical}}
	q := safe.NewQueue[*types.Event]()
	ins.checkHasLeader(q, "http://test:2379", "0")
	event := popEvent(q)
	if event.EventStatus != types.EventStatusCritical {
		t.Errorf("status = %s, want Critical", event.EventStatus)
	}
}

func TestCheckHasLeader_Empty(t *testing.T) {
	ins := &Instance{HasLeader: HasLeaderCheck{Severity: types.EventStatusCritical}}
	q := safe.NewQueue[*types.Event]()
	ins.checkHasLeader(q, "http://test:2379", "")
	event := popEvent(q)
	if event.EventStatus != types.EventStatusCritical {
		t.Errorf("status = %s, want Critical", event.EventStatus)
	}
}

// ---------------------------------------------------------------------------
// db_size_pct check
// ---------------------------------------------------------------------------

func TestCheckDbSizePct_OK(t *testing.T) {
	ins := &Instance{DbSizePct: DbSizePctCheck{
		WarnGe:     80,
		CriticalGe: 90,
	}}
	q := safe.NewQueue[*types.Event]()
	ins.checkDbSizePct(q, "http://test:2379", 1*1024*1024*1024, 2*1024*1024*1024) // 50%
	event := popEvent(q)
	if event.EventStatus != types.EventStatusOk {
		t.Errorf("status = %s, want Ok", event.EventStatus)
	}
}

func TestCheckDbSizePct_Warning(t *testing.T) {
	ins := &Instance{DbSizePct: DbSizePctCheck{
		WarnGe:     80,
		CriticalGe: 90,
	}}
	q := safe.NewQueue[*types.Event]()
	ins.checkDbSizePct(q, "http://test:2379", 1.7*1024*1024*1024, 2*1024*1024*1024) // ~85%
	event := popEvent(q)
	if event.EventStatus != types.EventStatusWarning {
		t.Errorf("status = %s, want Warning", event.EventStatus)
	}
}

func TestCheckDbSizePct_Critical(t *testing.T) {
	ins := &Instance{DbSizePct: DbSizePctCheck{
		WarnGe:     80,
		CriticalGe: 90,
	}}
	q := safe.NewQueue[*types.Event]()
	ins.checkDbSizePct(q, "http://test:2379", 1.9*1024*1024*1024, 2*1024*1024*1024) // ~95%
	event := popEvent(q)
	if event.EventStatus != types.EventStatusCritical {
		t.Errorf("status = %s, want Critical", event.EventStatus)
	}
}

func TestResolveQuota_Configured(t *testing.T) {
	ins := &Instance{DbSizePct: DbSizePctCheck{QuotaBackendBytes: 8 * config.GB}, cachedQuota: make(map[string]int64)}
	got := ins.resolveQuota("http://test:2379", nil)
	if got != int64(8*config.GB) {
		t.Errorf("resolveQuota = %d, want 8GB", got)
	}
}

func TestResolveQuota_AutoDetect(t *testing.T) {
	ins := &Instance{DbSizePct: DbSizePctCheck{}, cachedQuota: make(map[string]int64)}
	metrics := []byte("etcd_server_quota_backend_bytes 8.589934592e+09\n")
	got := ins.resolveQuota("http://test:2379", metrics)
	if got != 8589934592 {
		t.Errorf("resolveQuota = %d, want 8589934592", got)
	}
}

func TestResolveQuota_AutoDetect_CacheNoRepeatLog(t *testing.T) {
	ins := &Instance{DbSizePct: DbSizePctCheck{}, cachedQuota: make(map[string]int64)}
	metrics := []byte("etcd_server_quota_backend_bytes 8.589934592e+09\n")
	ins.resolveQuota("http://test:2379", metrics)
	if ins.cachedQuota["http://test:2379"] != 8589934592 {
		t.Errorf("cachedQuota not set after first call")
	}
	got := ins.resolveQuota("http://test:2379", metrics)
	if got != 8589934592 {
		t.Errorf("resolveQuota = %d on second call, want 8589934592", got)
	}
}

func TestResolveQuota_FallbackDefault(t *testing.T) {
	ins := &Instance{DbSizePct: DbSizePctCheck{}, cachedQuota: make(map[string]int64)}
	got := ins.resolveQuota("http://test:2379", nil)
	if got != defaultQuotaBackendBytes {
		t.Errorf("resolveQuota = %d, want %d (default)", got, defaultQuotaBackendBytes)
	}
}

// ---------------------------------------------------------------------------
// alarm check (with HTTP mock)
// ---------------------------------------------------------------------------

func TestGatherAlarms_NoAlarms(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"alarms": []interface{}{}})
	}))
	defer server.Close()

	ins := &Instance{
		Targets: []string{server.URL},
		Alarm:   AlarmCheck{Severity: types.EventStatusCritical},
		client:  server.Client(),
	}
	q := safe.NewQueue[*types.Event]()
	ins.gatherAlarms(q, server.URL)
	event := popEvent(q)
	if event.EventStatus != types.EventStatusOk {
		t.Errorf("status = %s, want Ok", event.EventStatus)
	}
}

func TestGatherAlarms_HasAlarm(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"alarms": []map[string]string{
				{"alarm": "NOSPACE", "memberID": "12345"},
			},
		})
	}))
	defer server.Close()

	ins := &Instance{
		Targets: []string{server.URL},
		Alarm:   AlarmCheck{Severity: types.EventStatusCritical},
		client:  server.Client(),
	}
	q := safe.NewQueue[*types.Event]()
	ins.gatherAlarms(q, server.URL)
	event := popEvent(q)
	if event.EventStatus != types.EventStatusCritical {
		t.Errorf("status = %s, want Critical", event.EventStatus)
	}
	if !strings.Contains(event.Description, "NOSPACE") {
		t.Errorf("desc should mention NOSPACE: %q", event.Description)
	}
}

func TestGatherAlarms_Disabled(t *testing.T) {
	ins := &Instance{
		Targets: []string{"http://test:2379"},
		Alarm:   AlarmCheck{Disabled: true},
	}
	q := safe.NewQueue[*types.Event]()
	ins.gatherAlarms(q, "http://test:2379")
	if q.Len() != 0 {
		t.Errorf("expected no events when alarm check is disabled, got %d", q.Len())
	}
}

// ---------------------------------------------------------------------------
// Delta counters
// ---------------------------------------------------------------------------

func TestDeltaCounters_Baseline(t *testing.T) {
	metrics := `etcd_server_slow_apply_total 10
etcd_server_leader_changes_seen_total 2
etcd_server_proposals_failed_total 0
`
	ins := &Instance{
		Targets:     []string{"http://test:2379"},
		SlowApply:   DeltaCounterCheck{WarnGe: 1},
		prevStats:   make(map[string]etcdCounterSnapshot),
		initialized: make(map[string]bool),
	}
	q := safe.NewQueue[*types.Event]()
	ins.gatherDeltaCounters(q, "http://test:2379", []byte(metrics))

	event := popEvent(q)
	if !strings.Contains(event.Description, "baseline") {
		t.Errorf("first gather should establish baseline: %q", event.Description)
	}
}

func TestDeltaCounters_Delta(t *testing.T) {
	ins := &Instance{
		Targets:   []string{"http://test:2379"},
		SlowApply: DeltaCounterCheck{WarnGe: 1, CriticalGe: 5},
		prevStats: map[string]etcdCounterSnapshot{
			"http://test:2379": {slowApply: 10},
		},
		initialized: map[string]bool{"http://test:2379": true},
	}

	metrics := `etcd_server_slow_apply_total 13
`
	q := safe.NewQueue[*types.Event]()
	ins.gatherDeltaCounters(q, "http://test:2379", []byte(metrics))

	event := popEvent(q)
	if event.EventStatus != types.EventStatusWarning {
		t.Errorf("status = %s, want Warning (delta=3)", event.EventStatus)
	}
	if !strings.Contains(event.Description, "delta 3") {
		t.Errorf("desc should mention delta 3: %q", event.Description)
	}
}

func TestDeltaCounters_Critical(t *testing.T) {
	ins := &Instance{
		Targets:   []string{"http://test:2379"},
		SlowApply: DeltaCounterCheck{WarnGe: 1, CriticalGe: 5},
		prevStats: map[string]etcdCounterSnapshot{
			"http://test:2379": {slowApply: 10},
		},
		initialized: map[string]bool{"http://test:2379": true},
	}

	metrics := `etcd_server_slow_apply_total 20
`
	q := safe.NewQueue[*types.Event]()
	ins.gatherDeltaCounters(q, "http://test:2379", []byte(metrics))

	event := popEvent(q)
	if event.EventStatus != types.EventStatusCritical {
		t.Errorf("status = %s, want Critical (delta=10)", event.EventStatus)
	}
}

// ---------------------------------------------------------------------------
// Full gather with HTTP mock
// ---------------------------------------------------------------------------

func TestGatherFullHTTP(t *testing.T) {
	mux := http.NewServeMux()

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"health":"true"}`)
	})

	mux.HandleFunc("/v3/maintenance/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// etcd gRPC gateway returns int64 fields as JSON strings
		json.NewEncoder(w).Encode(map[string]interface{}{
			"leader":      "12345",
			"dbSize":      fmt.Sprintf("%d", 500*1024*1024),
			"dbSizeInUse": fmt.Sprintf("%d", 400*1024*1024),
			"header":      map[string]interface{}{"member_id": "12345"},
		})
	})

	mux.HandleFunc("/v3/maintenance/alarm", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"alarms": []interface{}{}})
	})

	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `# HELP etcd_server_quota_backend_bytes Current backend storage quota size in bytes.
# TYPE etcd_server_quota_backend_bytes gauge
etcd_server_quota_backend_bytes 2.147483648e+09
# TYPE etcd_disk_backend_commit_duration_seconds histogram
etcd_disk_backend_commit_duration_seconds_bucket{le="0.001"} 0
etcd_disk_backend_commit_duration_seconds_bucket{le="0.002"} 100
etcd_disk_backend_commit_duration_seconds_bucket{le="0.004"} 500
etcd_disk_backend_commit_duration_seconds_bucket{le="0.008"} 900
etcd_disk_backend_commit_duration_seconds_bucket{le="0.016"} 990
etcd_disk_backend_commit_duration_seconds_bucket{le="0.032"} 1000
etcd_disk_backend_commit_duration_seconds_bucket{le="+Inf"} 1000
etcd_disk_backend_commit_duration_seconds_sum 3.5
etcd_disk_backend_commit_duration_seconds_count 1000
`)
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	ins := &Instance{
		Targets:     []string{server.URL},
		Concurrency: 1,
		QuantileChecks: []QuantileCheck{
			{Phi: 0.99, WarnGe: config.Duration(100 * time.Millisecond), CriticalGe: config.Duration(250 * time.Millisecond)},
		},
		HasLeader: HasLeaderCheck{Severity: types.EventStatusCritical},
		DbSizePct: DbSizePctCheck{
			WarnGe:     80,
			CriticalGe: 90,
		},
		Alarm:       AlarmCheck{Severity: types.EventStatusCritical},
		client:      server.Client(),
		prevStats:   make(map[string]etcdCounterSnapshot),
		initialized: make(map[string]bool),
		cachedQuota: make(map[string]int64),
	}
	ins.HealthPath = "/health"
	ins.MetricsPath = "/metrics"
	ins.HistogramMetric = "etcd_disk_backend_commit_duration_seconds"
	ins.WalFsyncMetric = "etcd_disk_wal_fsync_duration_seconds"
	ins.ConnectivitySeverity = types.EventStatusCritical

	q := safe.NewQueue[*types.Event]()
	ins.gather(q, server.URL)

	events := make(map[string]*types.Event)
	for q.Len() > 0 {
		e := popEvent(q)
		events[e.Labels["check"]] = e
	}

	// health OK
	if e, ok := events["etcd::health"]; !ok {
		t.Error("missing etcd::health event")
	} else if e.EventStatus != types.EventStatusOk {
		t.Errorf("health status = %s, want Ok", e.EventStatus)
	}

	// has_leader OK
	if e, ok := events["etcd::has_leader"]; !ok {
		t.Error("missing etcd::has_leader event")
	} else if e.EventStatus != types.EventStatusOk {
		t.Errorf("has_leader status = %s, want Ok", e.EventStatus)
	}

	// db_size_pct OK (~24%)
	if e, ok := events["etcd::db_size_pct"]; !ok {
		t.Error("missing etcd::db_size_pct event")
	} else if e.EventStatus != types.EventStatusOk {
		t.Errorf("db_size_pct status = %s, want Ok", e.EventStatus)
	}

	// alarm OK
	if e, ok := events["etcd::alarm"]; !ok {
		t.Error("missing etcd::alarm event")
	} else if e.EventStatus != types.EventStatusOk {
		t.Errorf("alarm status = %s, want Ok", e.EventStatus)
	}

	// backend commit quantile OK (p99 should be ~16ms range, well under 100ms)
	if e, ok := events["etcd::backend_commit_p99"]; !ok {
		t.Error("missing etcd::backend_commit_p99 event")
	} else if e.EventStatus != types.EventStatusOk {
		t.Errorf("backend_commit_p99 status = %s, want Ok", e.EventStatus)
	}
}

// ---------------------------------------------------------------------------
// Validation tests
// ---------------------------------------------------------------------------

func TestValidateDeltaCounterCheck(t *testing.T) {
	if err := validateDeltaCounterCheck("test", DeltaCounterCheck{WarnGe: 5, CriticalGe: 3}); err == nil {
		t.Error("expected error for warn_ge >= critical_ge")
	}
	if err := validateDeltaCounterCheck("test", DeltaCounterCheck{WarnGe: 3, CriticalGe: 5}); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateQuantileChecks(t *testing.T) {
	if err := validateQuantileChecks("test", []QuantileCheck{{Phi: 1.5}}); err == nil {
		t.Error("expected error for phi > 1")
	}
	if err := validateQuantileChecks("test", []QuantileCheck{
		{Phi: 0.99, WarnGe: config.Duration(500 * time.Millisecond), CriticalGe: config.Duration(100 * time.Millisecond)},
	}); err == nil {
		t.Error("expected error for warn_ge >= critical_ge")
	}
}

func TestSafeDelta(t *testing.T) {
	if safeDelta(10, 5) != 5 {
		t.Error("normal delta failed")
	}
	if safeDelta(5, 10) != 0 {
		t.Error("counter reset should return 0")
	}
}
