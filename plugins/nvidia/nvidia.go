package nvidia

import (
	"context"
	"time"

	"github.com/cprobe/catpaw/diagnose"
	"github.com/cprobe/catpaw/plugins"
)

const defaultTimeout = 15 * time.Second

// ctxTimeout returns the smaller of the context deadline remaining and fallback.
func ctxTimeout(ctx context.Context, fallback time.Duration) time.Duration {
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining > 0 && remaining < fallback {
			return remaining
		}
	}
	return fallback
}

func init() {
	plugins.AddDiagnoseRegistrar(func(registry *diagnose.ToolRegistry) {
		registerPeermem(registry)
		registerGPULink(registry)
	})
}
