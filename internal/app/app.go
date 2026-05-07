package app

import (
	"context"
	"fmt"
	"log/slog"

	"alarm-service/internal/alarm"
	"alarm-service/internal/fsm"
	"alarm-service/internal/pm"
	"alarm-service/internal/redis"
)

// Config holds application configuration. The chip-config flags
// (--i2c-bus, --evdev-*, --poller-interval-ms) are gone — motion-service
// owns the BMX055 now and alarm-service is a pure consumer of motion
// events + the synchronous prepare-hibernation handshake.
type Config struct {
	RedisAddr                  string
	Logger                     *slog.Logger
	AlarmEnabled               bool
	AlarmEnabledFlagSet        bool
	AlarmDuration              int
	DurationFlagSet            bool
	HornEnabled                bool
	HornFlagSet                bool
	SeatboxTrigger             bool
	SeatboxTriggerFlagSet      bool
	HairTrigger                bool
	HairTriggerFlagSet         bool
	HairTriggerDuration        int
	HairTriggerDurationFlagSet bool
	L1Cooldown                 int
	L1CooldownFlagSet          bool
}

// App represents the alarm-service application.
type App struct {
	cfg             *Config
	log             *slog.Logger
	redis           *redis.Client
	publisher       *redis.Publisher
	motion          *redis.MotionClient
	alarmController *alarm.Controller
	inhibitor       *pm.Inhibitor
	stateMachine    *fsm.StateMachine
	subscriber      *redis.Subscriber
}

// New creates a new App.
func New(cfg *Config) *App {
	return &App{
		cfg: cfg,
		log: cfg.Logger,
	}
}

// Run runs the application.
func (a *App) Run(ctx context.Context) error {
	a.log.Info("starting alarm-service", "redis_addr", a.cfg.RedisAddr)

	var err error
	a.redis, err = redis.NewClient(a.cfg.RedisAddr, a.log)
	if err != nil {
		return fmt.Errorf("create redis client: %w", err)
	}
	if err := a.redis.Connect(ctx); err != nil {
		return fmt.Errorf("connect to redis: %w", err)
	}
	defer a.redis.Close()

	a.publisher = redis.NewPublisher(a.redis)
	a.motion, err = redis.NewMotionClient(a.redis.IPC())
	if err != nil {
		return fmt.Errorf("create motion client: %w", err)
	}
	defer a.motion.Close()

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
		a.motion,
		a.publisher,
		a.inhibitor,
		a.alarmController,
		a.publisher,
		a.cfg.AlarmDuration,
		a.log,
	)

	a.alarmController.SetCommander(a.stateMachine)

	a.subscriber = redis.NewSubscriber(a.redis, a.stateMachine, a.log)

	// Read motion-service's wake-cause stamp before anything else writes
	// to motion. Persistent so this works even when motion-service
	// stamped + published wake-hibernation before alarm-service had its
	// subscriber up — the hash field is the durable backstop for that
	// startup-ordering race.
	if woke, err := a.motion.ConsumeWakeCause(ctx); err != nil {
		a.log.Warn("consume motion.wake-cause failed", "error", err)
	} else if woke {
		a.log.Info("woke from hibernation motion (stamp from motion-service)")
		a.stateMachine.SendEvent(fsm.BMXInterruptEvent{Data: "wake-hibernation"})
	}

	if err := a.handleCLIOverrides(); err != nil {
		a.log.Warn("failed to handle CLI overrides", "error", err)
	}

	if err := a.subscriber.Start(); err != nil {
		return fmt.Errorf("start subscriber: %w", err)
	}
	defer a.subscriber.Stop()

	go a.stateMachine.Run(ctx)

	<-ctx.Done()
	a.log.Info("shutting down")
	return nil
}

// handleCLIOverrides handles CLI flag overrides for settings.
func (a *App) handleCLIOverrides() error {
	settingsPub := a.redis.IPC().NewHashPublisher("settings")

	if a.cfg.AlarmEnabledFlagSet {
		a.log.Info("alarm-enabled flag set, writing to Redis", "enabled", a.cfg.AlarmEnabled)
		value := "false"
		if a.cfg.AlarmEnabled {
			value = "true"
		}
		if err := settingsPub.Set("alarm.enabled", value); err != nil {
			return fmt.Errorf("failed to set alarm.enabled: %w", err)
		}
	}

	if a.cfg.HornFlagSet {
		a.log.Info("horn flag set, writing to Redis", "enabled", a.cfg.HornEnabled)
		hornValue := "false"
		if a.cfg.HornEnabled {
			hornValue = "true"
		}
		if err := settingsPub.Set("alarm.honk", hornValue); err != nil {
			return fmt.Errorf("failed to set alarm.honk: %w", err)
		}
	}

	if a.cfg.DurationFlagSet {
		a.log.Info("duration flag set, writing to Redis", "duration", a.cfg.AlarmDuration)
		durationValue := fmt.Sprintf("%d", a.cfg.AlarmDuration)
		if err := settingsPub.Set("alarm.duration", durationValue); err != nil {
			return fmt.Errorf("failed to set alarm.duration: %w", err)
		}
	}

	if a.cfg.SeatboxTriggerFlagSet {
		a.log.Info("seatbox-trigger flag set, writing to Redis", "enabled", a.cfg.SeatboxTrigger)
		seatboxValue := "false"
		if a.cfg.SeatboxTrigger {
			seatboxValue = "true"
		}
		if err := settingsPub.Set("alarm.seatbox-trigger", seatboxValue); err != nil {
			return fmt.Errorf("failed to set alarm.seatbox-trigger: %w", err)
		}
	}

	if a.cfg.HairTriggerFlagSet {
		a.log.Info("hair-trigger flag set, writing to Redis", "enabled", a.cfg.HairTrigger)
		value := "false"
		if a.cfg.HairTrigger {
			value = "true"
		}
		if err := settingsPub.Set("alarm.hairtrigger", value); err != nil {
			return fmt.Errorf("failed to set alarm.hairtrigger: %w", err)
		}
	}

	if a.cfg.HairTriggerDurationFlagSet {
		a.log.Info("hair-trigger-duration flag set, writing to Redis", "duration", a.cfg.HairTriggerDuration)
		value := fmt.Sprintf("%d", a.cfg.HairTriggerDuration)
		if err := settingsPub.Set("alarm.hairtrigger-duration", value); err != nil {
			return fmt.Errorf("failed to set alarm.hairtrigger-duration: %w", err)
		}
	}

	if a.cfg.L1CooldownFlagSet {
		a.log.Info("l1-cooldown flag set, writing to Redis", "duration", a.cfg.L1Cooldown)
		value := fmt.Sprintf("%d", a.cfg.L1Cooldown)
		if err := settingsPub.Set("alarm.l1-cooldown", value); err != nil {
			return fmt.Errorf("failed to set alarm.l1-cooldown: %w", err)
		}
	}

	return nil
}
