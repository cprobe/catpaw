package logger

import (
	"time"

	"flashcat.cloud/catpaw/config"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var Logger *zap.SugaredLogger

func Build() func() {
	c := config.Config.LogConfig

	loggerConfig := zap.NewProductionConfig()
	loggerConfig.EncoderConfig.TimeKey = "ts"
	loggerConfig.EncoderConfig.EncodeTime = zapcore.TimeEncoderOfLayout(time.RFC3339)
	loggerConfig.DisableStacktrace = true

	switch c.Level {
	case "debug":
		loggerConfig.Level = zap.NewAtomicLevelAt(zap.DebugLevel)
	case "info":
		loggerConfig.Level = zap.NewAtomicLevelAt(zap.InfoLevel)
	case "warn":
		loggerConfig.Level = zap.NewAtomicLevelAt(zap.WarnLevel)
	case "error":
		loggerConfig.Level = zap.NewAtomicLevelAt(zap.ErrorLevel)
	case "fatal":
		loggerConfig.Level = zap.NewAtomicLevelAt(zap.FatalLevel)
	default:
		loggerConfig.Level = zap.NewAtomicLevelAt(zap.InfoLevel)
	}

	loggerConfig.Encoding = c.Format
	loggerConfig.OutputPaths = []string{c.Output}
	loggerConfig.InitialFields = c.Fields

	logger, err := loggerConfig.Build()
	if err != nil {
		panic(err)
	}

	Logger = logger.Sugar()

	return func() { Logger.Sync() }
}
