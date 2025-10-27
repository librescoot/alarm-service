package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"alarm-service/internal/alarm"
	"alarm-service/internal/bmx"
	"alarm-service/internal/fsm"
	"alarm-service/internal/hardware"
	hwbmx "alarm-service/internal/hardware/bmx"
	"alarm-service/internal/hardware/driver"
	"alarm-service/internal/pm"
	"alarm-service/internal/redis"
)

// Config holds application configuration
type Config struct {
	I2CBus          string
	RedisAddr       string
	Logger          *slog.Logger
	AlarmDuration   int
	DurationFlagSet bool
	HornEnabled     bool
	HornFlagSet     bool
}

// App represents the alarm-service application
type App struct {
	cfg              *Config
	log              *slog.Logger
	redis            *redis.Client
	publisher        *redis.Publisher
	accel            *hwbmx.Accelerometer
	gyro             *hwbmx.Gyroscope
	bmxController    *bmx.HardwareController
	interruptPoller  *hardware.InterruptPoller
	alarmController  *alarm.Controller
	inhibitor        *pm.Inhibitor
	stateMachine     *fsm.StateMachine
	subscriber       *redis.Subscriber
}

// New creates a new App
func New(cfg *Config) *App {
	return &App{
		cfg: cfg,
		log: cfg.Logger,
	}
}

// Run runs the application
func (a *App) Run(ctx context.Context) error {
	a.log.Info("starting alarm-service",
		"i2c_bus", a.cfg.I2CBus,
		"redis_addr", a.cfg.RedisAddr)

	if err := a.unbindDrivers(); err != nil {
		return fmt.Errorf("unbind drivers: %w", err)
	}

	a.redis = redis.NewClient(a.cfg.RedisAddr, a.log)
	if err := a.redis.Connect(ctx); err != nil {
		return fmt.Errorf("connect to redis: %w", err)
	}
	defer a.redis.Close()

	a.publisher = redis.NewPublisher(a.redis)

	if err := a.initBMXHardware(); err != nil {
		return fmt.Errorf("init bmx hardware: %w", err)
	}
	defer a.closeBMXHardware()

	a.interruptPoller = hardware.NewInterruptPoller(a.accel, a.gyro, a.publisher, a.log)
	go a.interruptPoller.Run(ctx)

	a.bmxController = bmx.NewHardwareController(a.accel, a.gyro, a.interruptPoller, a.log)

	var err error
	a.alarmController, err = alarm.NewController(a.cfg.RedisAddr, a.cfg.HornEnabled, a.log)
	if err != nil {
		return fmt.Errorf("create alarm controller: %w", err)
	}
	defer a.alarmController.Close()

	a.inhibitor, err = pm.NewInhibitor(a.log)
	if err != nil {
		return fmt.Errorf("create suspend inhibitor: %w", err)
	}
	defer a.inhibitor.Close()

	a.stateMachine = fsm.New(
		a.bmxController,
		a.publisher,
		a.inhibitor,
		a.alarmController,
		a.cfg.AlarmDuration,
		a.log,
	)

	a.subscriber = redis.NewSubscriber(a.redis, a.stateMachine, a.log)

	if err := a.publishInitialStatus(ctx); err != nil {
		a.log.Warn("failed to publish initial BMX status", "error", err)
	}

	if a.cfg.HornFlagSet {
		a.log.Info("horn flag set, writing to Redis", "enabled", a.cfg.HornEnabled)
		hornValue := "false"
		if a.cfg.HornEnabled {
			hornValue = "true"
		}
		if err := a.redis.HSet(ctx, "settings", "alarm.honk", hornValue); err != nil {
			a.log.Error("failed to write alarm.honk to Redis", "error", err)
		}
		if err := a.redis.Publish(ctx, "settings", "alarm.honk"); err != nil {
			a.log.Error("failed to publish alarm.honk change", "error", err)
		}
	}

	if a.cfg.DurationFlagSet {
		a.log.Info("duration flag set, writing to Redis", "duration", a.cfg.AlarmDuration)
		durationValue := fmt.Sprintf("%d", a.cfg.AlarmDuration)
		if err := a.redis.HSet(ctx, "settings", "alarm.duration", durationValue); err != nil {
			a.log.Error("failed to write alarm.duration to Redis", "error", err)
		}
		if err := a.redis.Publish(ctx, "settings", "alarm.duration"); err != nil {
			a.log.Error("failed to publish alarm.duration change", "error", err)
		}
	}

	a.subscriber.CheckInitialState(ctx)

	go a.stateMachine.Run(ctx)
	go a.subscriber.SubscribeToVehicleState(ctx)
	go a.subscriber.SubscribeToAlarmSettings(ctx)
	go a.subscriber.SubscribeToBMXInterrupt(ctx)
	go a.alarmController.ListenForCommands(ctx)

	<-ctx.Done()
	a.log.Info("shutting down")
	return nil
}

// unbindDrivers unbinds kernel drivers
func (a *App) unbindDrivers() error {
	a.log.Info("unbinding kernel drivers")

	if err := driver.UnbindBMX055(); err != nil {
		a.log.Warn("failed to unbind BMX055 drivers", "error", err)
	}

	time.Sleep(100 * time.Millisecond)
	return nil
}

// initBMXHardware initializes the BMX hardware
func (a *App) initBMXHardware() error {
	var err error

	a.log.Info("initializing accelerometer")
	a.accel, err = hwbmx.NewAccelerometer(a.cfg.I2CBus)
	if err != nil {
		return fmt.Errorf("init accelerometer: %w", err)
	}

	a.log.Info("initializing gyroscope")
	a.gyro, err = hwbmx.NewGyroscope(a.cfg.I2CBus)
	if err != nil {
		return fmt.Errorf("init gyroscope: %w", err)
	}

	a.log.Info("BMX hardware initialized")
	return nil
}

// closeBMXHardware closes the BMX hardware
func (a *App) closeBMXHardware() {
	if a.accel != nil {
		a.accel.Close()
	}
	if a.gyro != nil {
		a.gyro.Close()
	}
}

// publishInitialStatus publishes initial BMX status to Redis
func (a *App) publishInitialStatus(ctx context.Context) error {
	if err := a.redis.HSet(ctx, "bmx", "initialized", "true"); err != nil {
		return err
	}
	if err := a.redis.HSet(ctx, "bmx", "interrupt", "disabled"); err != nil {
		return err
	}
	if err := a.redis.HSet(ctx, "bmx", "sensitivity", "none"); err != nil {
		return err
	}
	if err := a.redis.HSet(ctx, "bmx", "pin", "none"); err != nil {
		return err
	}
	return nil
}