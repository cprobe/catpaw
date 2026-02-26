//go:build windows

package main

import (
	"os"
	"os/signal"
	"syscall"

	"flashcat.cloud/catpaw/agent"
	"flashcat.cloud/catpaw/logger"
)

func waitForSignal(ag *agent.Agent) {
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM)

	for {
		sig := <-sc
		logger.Logger.Infow("received signal", "signal", sig.String())
		switch sig {
		case syscall.SIGTERM, syscall.SIGINT:
			return
		}
	}
}
