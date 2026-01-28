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
	i2cBus := flag.String("i2c-bus", "/dev/i2c-3", "I2C bus device path")
	redisAddr := flag.String("redis", "localhost:6379", "Redis address")
	logLevel := flag.String("log-level", "info", "Log level: debug, info, warn, error")
	alarmEnabled := flag.Bool("alarm-enabled", false, "Enable alarm system (writes to Redis on startup)")
	alarmDuration := flag.Int("alarm-duration", 10, "Alarm duration in seconds")
	hornEnabled := flag.Bool("horn-enabled", false, "Enable horn during alarm")
	seatboxTrigger := flag.Bool("seatbox-trigger", true, "Trigger alarm on unauthorized seatbox opening")
	hairTrigger := flag.Bool("hair-trigger", false, "Enable hair trigger mode (immediate short alarm on first motion)")
	hairTriggerDuration := flag.Int("hair-trigger-duration", 3, "Hair trigger alarm duration in seconds")
	l1Cooldown := flag.Int("l1-cooldown", 15, "Level 1 cooldown duration in seconds")
	versionFlag := flag.Bool("version", false, "Print version and exit")
	flag.Parse()

	alarmEnabledFlagSet := false
	hornFlagSet := false
	durationFlagSet := false
	seatboxTriggerFlagSet := false
	hairTriggerFlagSet := false
	hairTriggerDurationFlagSet := false
	l1CooldownFlagSet := false
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
	})

	if *versionFlag {
		fmt.Printf("alarm-service %s\n", version)
		os.Exit(0)
	}

	level := parseLogLevel(*logLevel)
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
	}))
	slog.SetDefault(logger)

	logger.Info("librescoot-alarm "+version+" starting",
		"i2c_bus", *i2cBus,
		"redis", *redisAddr,
		"log_level", *logLevel,
		"alarm_enabled", *alarmEnabled,
		"alarm_duration", *alarmDuration,
		"horn_enabled", *hornEnabled,
		"seatbox_trigger", *seatboxTrigger,
		"hair_trigger", *hairTrigger,
		"hair_trigger_duration", *hairTriggerDuration,
		"l1_cooldown", *l1Cooldown)

	application := app.New(&app.Config{
		I2CBus:                     *i2cBus,
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
