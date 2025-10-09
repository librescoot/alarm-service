package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"alarm-service/internal/app"
)

var (
	gitRevision = "unknown"
	buildTime   = "unknown"
)

func main() {
	redisAddr := flag.String("redis", "localhost:6379", "Redis address")
	logLevel := flag.String("log-level", "info", "Log level: debug, info, warn, error")
	alarmDuration := flag.Int("alarm-duration", 10, "Alarm duration in seconds")
	hornEnabled := flag.Bool("horn-enabled", false, "Enable horn during alarm")
	version := flag.Bool("version", false, "Print version and exit")
	flag.Parse()

	hornFlagSet := false
	durationFlagSet := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "horn-enabled" {
			hornFlagSet = true
		}
		if f.Name == "alarm-duration" {
			durationFlagSet = true
		}
	})

	if *version {
		println("alarm-service")
		println("  Revision:", gitRevision)
		println("  Built:", buildTime)
		os.Exit(0)
	}

	level := parseLogLevel(*logLevel)
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
	}))
	slog.SetDefault(logger)

	logger.Info("alarm-service starting",
		"revision", gitRevision,
		"build_time", buildTime,
		"redis", *redisAddr,
		"log_level", *logLevel,
		"alarm_duration", *alarmDuration,
		"horn_enabled", *hornEnabled)

	application := app.New(&app.Config{
		RedisAddr:        *redisAddr,
		Logger:           logger,
		AlarmDuration:    *alarmDuration,
		DurationFlagSet:  durationFlagSet,
		HornEnabled:      *hornEnabled,
		HornFlagSet:      hornFlagSet,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	errChan := make(chan error, 1)
	go func() {
		errChan <- application.Run(ctx)
	}()

	select {
	case sig := <-sigChan:
		logger.Info("received signal", "signal", sig)
		cancel()
		<-errChan

	case err := <-errChan:
		if err != nil {
			logger.Error("application error", "error", err)
			os.Exit(1)
		}
	}

	logger.Info("alarm-service stopped")
}

func parseLogLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}