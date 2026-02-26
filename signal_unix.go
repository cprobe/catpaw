//go:build !windows

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
	signal.Notify(sc, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGPIPE)

	for {
		sig := <-sc
		logger.Logger.Infow("received signal", "signal", sig.String())
		switch sig {
		case syscall.SIGQUIT, syscall.SIGTERM, syscall.SIGINT:
			return
		case syscall.SIGHUP:
			ag.Reload()
		case syscall.SIGPIPE:
			// https://pkg.go.dev/os/signal#hdr-SIGPIPE
		}
	}
}
