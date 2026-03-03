package diagnose

import (
	"sync"
	"time"

	"github.com/cprobe/catpaw/config"
	"github.com/cprobe/catpaw/logger"
)

var (
	globalMu         sync.RWMutex
	globalEngine     *DiagnoseEngine
	globalAggregator *DiagnoseAggregator
	cleanupStop      chan struct{}
)

// Init initializes the global diagnose engine and aggregator.
// Called once at startup from the agent package.
func Init(registry *ToolRegistry) {
	cfg := config.Config.AI
	if !cfg.Enabled {
		logger.Logger.Infow("AI diagnose disabled")
		return
	}

	globalMu.Lock()
	defer globalMu.Unlock()

	globalEngine = NewDiagnoseEngine(registry, cfg)
	globalAggregator = NewDiagnoseAggregator(globalEngine, time.Duration(cfg.AggregateWindow))

	CleanupRecords()
	cleanupStop = make(chan struct{})
	go runPeriodicCleanup(cleanupStop)

	logger.Logger.Infow("AI diagnose engine initialized",
		"models", cfg.ModelPriority,
		"max_rounds", cfg.MaxRounds,
		"max_concurrent", cfg.MaxConcurrentDiagnoses,
		"aggregate_window", time.Duration(cfg.AggregateWindow),
		"tools", registry.ToolCount(),
	)
}

// GlobalAggregator returns the singleton aggregator, or nil if not initialized.
func GlobalAggregator() *DiagnoseAggregator {
	globalMu.RLock()
	defer globalMu.RUnlock()
	return globalAggregator
}

// GlobalEngine returns the singleton engine, or nil if not initialized.
func GlobalEngine() *DiagnoseEngine {
	globalMu.RLock()
	defer globalMu.RUnlock()
	return globalEngine
}

// Shutdown gracefully stops the diagnose subsystem.
func Shutdown() {
	globalMu.Lock()
	defer globalMu.Unlock()

	if cleanupStop != nil {
		close(cleanupStop)
		cleanupStop = nil
	}
	if globalAggregator != nil {
		globalAggregator.Shutdown()
	}
	if globalEngine != nil {
		globalEngine.Shutdown()
	}

	logger.Logger.Infow("AI diagnose engine shutdown complete")
}

func runPeriodicCleanup(stop chan struct{}) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			CleanupRecords()
		}
	}
}
