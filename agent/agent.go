package agent

import (
	"flashcat.cloud/catpaw/duty"
	"go.uber.org/zap"
)

type Agent struct {
	logger     *zap.SugaredLogger
	dutyClient *duty.Duty
}

func NewAgent(logger *zap.SugaredLogger, dutyClient *duty.Duty) *Agent {
	return &Agent{
		logger:     logger,
		dutyClient: dutyClient,
	}
}

func (a *Agent) Start() {
	a.logger.Info("agent starting")
	a.logger.Info("agent started")
}
func (a *Agent) Stop() {
	a.logger.Info("agent stopping")
	a.logger.Info("agent stopped")
}
func (a *Agent) Reload() {
	a.logger.Info("agent reloading")
	a.logger.Info("agent reloaded")
}
