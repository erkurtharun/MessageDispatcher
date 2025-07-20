// Package logger provides a structured logging system using Zap, optimized for microservices.
// It supports JSON output with Elastic Common Schema (ECS) fields for seamless ELK (ElasticSearch, Logstash, Kibana) integration.
// Best practices: Low-allocation production mode, sampling for high-volume logs, dynamic level via env (LOG_LEVEL),
// context-aware fields (e.g., correlation ID as trace.id), initial fields (e.g., service.name), and graceful sync on shutdown.
package logger

import (
	"os"
	"strings"

	"go.elastic.co/ecszap"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var globalLogger *zap.Logger

// Init initializes the logger based on environment and returns it with any error.
// Uses production config by default (JSON, ECS fields); development mode if LOG_ENV=dev.
// Log level from LOG_LEVEL env (debug, info, warn, error; default info).
func Init() (*zap.Logger, error) {
	levelStr := strings.ToLower(os.Getenv("LOG_LEVEL"))
	var level zapcore.Level
	switch levelStr {
	case "debug":
		level = zap.DebugLevel
	case "warn":
		level = zap.WarnLevel
	case "error":
		level = zap.ErrorLevel
	default:
		level = zap.InfoLevel
	}

	var config zap.Config
	env := strings.ToLower(os.Getenv("LOG_ENV"))
	if env == "dev" || env == "development" {
		config = zap.NewDevelopmentConfig()
		config.Level = zap.NewAtomicLevelAt(level)
		config.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder // Colored for console
	} else {
		config = zap.NewProductionConfig()
		config.Level = zap.NewAtomicLevelAt(level)
		config.InitialFields = map[string]interface{}{
			"service.name": os.Getenv("SERVICE_NAME"),
		}
		config.Sampling = &zap.SamplingConfig{ // Prevent log floods
			Initial:    100,
			Thereafter: 100,
		}
		config.OutputPaths = []string{"stdout"} // For ELK shipping via Filebeat
	}

	// ECS encoder setup
	encoderConfig := ecszap.NewDefaultEncoderConfig()
	core := ecszap.NewCore(encoderConfig, zapcore.AddSync(os.Stdout), config.Level)

	logger, err := config.Build(zap.WrapCore(func(zapcore.Core) zapcore.Core {
		return core
	}), zap.AddCaller(), zap.AddStacktrace(zap.ErrorLevel))
	if err != nil {
		return nil, err // Caller handles, e.g., print to stderr and exit
	}

	globalLogger = logger
	return globalLogger, nil
}

func WithCorrID(corrID string) *zap.Logger {
	return globalLogger.With(zap.String("trace.id", corrID))
}
