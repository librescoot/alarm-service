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
	version := flag.Bool("version", false, "Print version and exit")
	flag.Parse()

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
		"log_level", *logLevel)

	application := app.New(&app.Config{
		RedisAddr: *redisAddr,
		Logger:    logger,
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