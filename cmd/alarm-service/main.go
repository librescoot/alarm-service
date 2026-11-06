package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"alarm-service/internal/app"
)

var version = "dev"

func main() {
	redisAddr := flag.String("redis", "localhost:6379", "Redis address")
	logLevel := flag.String("log-level", "info", "Log level: debug, info, warn, error")
	alarmEnabled := flag.Bool("alarm-enabled", true, "Enable alarm system (writes to Redis on startup)")
	alarmDuration := flag.Int("alarm-duration", 30, "Duration of each full alarm burst in seconds")
	hornEnabled := flag.Bool("horn-enabled", false, "Enable horn during alarm")
	seatboxTrigger := flag.Bool("seatbox-trigger", true, "Trigger alarm on unauthorized seatbox opening")
	hairTrigger := flag.Bool("hair-trigger", false, "Sound a short first-trigger alarm after the 750 ms authentication grace")
	hairTriggerDuration := flag.Int("hair-trigger-duration", 3, "Hair trigger alarm duration in seconds")
	l1Cooldown := flag.Int("l1-cooldown", 2, "Minimum delay before another trigger can escalate to the full alarm, in seconds")
	quietTime := flag.Int("quiet-time", 30, "Seconds without a trigger before rearming after an alarm episode (1-300)")
	versionFlag := flag.Bool("version", false, "Print version and exit")
	flag.Parse()

	alarmEnabledFlagSet := false
	hornFlagSet := false
	durationFlagSet := false
	seatboxTriggerFlagSet := false
	hairTriggerFlagSet := false
	hairTriggerDurationFlagSet := false
	l1CooldownFlagSet := false
	quietTimeFlagSet := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "alarm-enabled" {
			alarmEnabledFlagSet = true
		}
		if f.Name == "horn-enabled" {
			hornFlagSet = true
		}
		if f.Name == "alarm-duration" {
			durationFlagSet = true
		}
		if f.Name == "seatbox-trigger" {
			seatboxTriggerFlagSet = true
		}
		if f.Name == "hair-trigger" {
			hairTriggerFlagSet = true
		}
		if f.Name == "hair-trigger-duration" {
			hairTriggerDurationFlagSet = true
		}
		if f.Name == "l1-cooldown" {
			l1CooldownFlagSet = true
		}
		if f.Name == "quiet-time" {
			quietTimeFlagSet = true
		}
	})

	if *versionFlag {
		fmt.Printf("alarm-service %s\n", version)
		os.Exit(0)
	}

	if *quietTime < 1 || *quietTime > 300 {
		fmt.Fprintln(os.Stderr, "quiet-time must be between 1 and 300 seconds")
		os.Exit(2)
	}

	level := parseLogLevel(*logLevel)
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
	}))
	slog.SetDefault(logger)

	logger.Info("librescoot-alarm "+version+" starting",
		"redis", *redisAddr,
		"log_level", *logLevel,
		"alarm_enabled", *alarmEnabled,
		"alarm_duration", *alarmDuration,
		"horn_enabled", *hornEnabled,
		"seatbox_trigger", *seatboxTrigger,
		"hair_trigger", *hairTrigger,
		"hair_trigger_duration", *hairTriggerDuration,
		"l2_escalation_delay", *l1Cooldown,
		"quiet_time", *quietTime)

	application := app.New(&app.Config{
		RedisAddr:                  *redisAddr,
		Logger:                     logger,
		AlarmEnabled:               *alarmEnabled,
		AlarmEnabledFlagSet:        alarmEnabledFlagSet,
		AlarmDuration:              *alarmDuration,
		DurationFlagSet:            durationFlagSet,
		HornEnabled:                *hornEnabled,
		HornFlagSet:                hornFlagSet,
		SeatboxTrigger:             *seatboxTrigger,
		SeatboxTriggerFlagSet:      seatboxTriggerFlagSet,
		HairTrigger:                *hairTrigger,
		HairTriggerFlagSet:         hairTriggerFlagSet,
		HairTriggerDuration:        *hairTriggerDuration,
		HairTriggerDurationFlagSet: hairTriggerDurationFlagSet,
		L1Cooldown:                 *l1Cooldown,
		L1CooldownFlagSet:          l1CooldownFlagSet,
		QuietTime:                  *quietTime,
		QuietTimeFlagSet:           quietTimeFlagSet,
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
