package diagnose

import (
	"sync"
	"time"

	"github.com/cprobe/catpaw/config"
	"github.com/cprobe/catpaw/logger"
	"github.com/cprobe/catpaw/types"
)

// DiagnoseAggregator collects alerts for the same target within a short
// time window, then submits one aggregated DiagnoseRequest to the engine.
type DiagnoseAggregator struct {
	mu      sync.Mutex
	pending map[string]*DiagnoseRequest // key: "plugin::target"
	timers  map[string]*time.Timer
	window  time.Duration
	engine  *DiagnoseEngine
}

// NewDiagnoseAggregator creates an aggregator with the given window duration.
func NewDiagnoseAggregator(engine *DiagnoseEngine, window time.Duration) *DiagnoseAggregator {
	if window <= 0 {
		window = 5 * time.Second
	}
	return &DiagnoseAggregator{
		pending: make(map[string]*DiagnoseRequest),
		timers:  make(map[string]*time.Timer),
		window:  window,
		engine:  engine,
	}
}

// Submit is called from the alerting engine when an alert event is produced.
// It aggregates events for the same (plugin, target) within the time window.
func (a *DiagnoseAggregator) Submit(event *types.Event, snapshot CheckSnapshot, pluginName string, instanceRef any, diagnoseConfig config.DiagnoseConfig) {
	if !config.Config.AI.Enabled {
		return
	}

	if event.EventStatus == types.EventStatusOk {
		return
	}

	if !shouldTrigger(diagnoseConfig, event.EventStatus) {
		return
	}

	target := event.Labels["target"]
	if target == "" {
		target = pluginName
	}
	key := pluginName + "::" + target

	a.mu.Lock()
	defer a.mu.Unlock()

	if req, exists := a.pending[key]; exists {
		req.Events = append(req.Events, event)
		req.Checks = append(req.Checks, snapshot)
		return
	}

	timeout := time.Duration(diagnoseConfig.Timeout)
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	cooldown := time.Duration(diagnoseConfig.Cooldown)
	if cooldown == 0 {
		cooldown = 10 * time.Minute
	}

	req := &DiagnoseRequest{
		Events:      []*types.Event{event},
		Plugin:      pluginName,
		Target:      target,
		Checks:      []CheckSnapshot{snapshot},
		InstanceRef: instanceRef,
		Timeout:     timeout,
		Cooldown:    cooldown,
	}
	a.pending[key] = req

	a.timers[key] = time.AfterFunc(a.window, func() {
		a.mu.Lock()
		req := a.pending[key]
		delete(a.pending, key)
		delete(a.timers, key)
		a.mu.Unlock()

		if req == nil {
			return
		}

		logger.Logger.Infow("diagnose aggregator: window closed, submitting",
			"key", key, "checks", len(req.Checks))
		a.engine.Submit(req)
	})
}

// Shutdown cancels all pending aggregation timers.
func (a *DiagnoseAggregator) Shutdown() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for key, timer := range a.timers {
		timer.Stop()
		delete(a.timers, key)
		delete(a.pending, key)
	}
}

func shouldTrigger(cfg config.DiagnoseConfig, eventStatus string) bool {
	if !cfg.Enabled {
		return false
	}
	minSeverity := cfg.MinSeverity
	if minSeverity == "" {
		minSeverity = "Warning"
	}
	return SeverityRank(eventStatus) >= SeverityRank(minSeverity)
}

// ExtractCheckSnapshot builds a CheckSnapshot from an event's labels.
func ExtractCheckSnapshot(event *types.Event) CheckSnapshot {
	return CheckSnapshot{
		Check:             event.Labels["check"],
		Status:            event.EventStatus,
		CurrentValue:      extractAttr(event, "current_value", ""),
		WarningThreshold:  extractAttr(event, "warn_ge", extractAttr(event, "warn_lt", "")),
		CriticalThreshold: extractAttr(event, "critical_ge", extractAttr(event, "critical_lt", "")),
		Description:       event.Description,
	}
}

func extractAttr(event *types.Event, key, fallback string) string {
	if v, ok := event.Labels[types.AttrPrefix+key]; ok && v != "" {
		return v
	}
	return fallback
}
