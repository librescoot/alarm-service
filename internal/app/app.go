package app

import (
	"context"
	"fmt"
	"log/slog"

	"alarm-service/internal/alarm"
	"alarm-service/internal/bmx"
	"alarm-service/internal/fsm"
	"alarm-service/internal/pm"
	"alarm-service/internal/redis"
)

// Config holds application configuration
type Config struct {
	RedisAddr string
	Logger    *slog.Logger
}

// App represents the alarm-service application
type App struct {
	cfg             *Config
	log             *slog.Logger
	redis           *redis.Client
	publisher       *redis.Publisher
	bmxClient       *bmx.Client
	alarmController *alarm.Controller
	inhibitor       *pm.Inhibitor
	stateMachine    *fsm.StateMachine
	subscriber      *redis.Subscriber
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
	a.log.Info("starting alarm-service", "redis_addr", a.cfg.RedisAddr)

	a.redis = redis.NewClient(a.cfg.RedisAddr, a.log)
	if err := a.redis.Connect(ctx); err != nil {
		return fmt.Errorf("connect to redis: %w", err)
	}
	defer a.redis.Close()

	a.publisher = redis.NewPublisher(a.redis)

	var err error
	a.bmxClient, err = bmx.NewClient(a.cfg.RedisAddr, a.log)
	if err != nil {
		return fmt.Errorf("create bmx client: %w", err)
	}
	defer a.bmxClient.Close()

	a.alarmController, err = alarm.NewController(a.cfg.RedisAddr, a.log)
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
		a.bmxClient,
		a.publisher,
		a.inhibitor,
		a.alarmController,
		a.log,
	)

	a.subscriber = redis.NewSubscriber(a.redis, a.stateMachine, a.log)

	a.subscriber.CheckInitialState(ctx)

	go a.stateMachine.Run(ctx)
	go a.subscriber.SubscribeToVehicleState(ctx)
	go a.subscriber.SubscribeToAlarmMode(ctx)
	go a.subscriber.SubscribeToBMXInterrupt(ctx)
	go a.alarmController.ListenForCommands(ctx)

	<-ctx.Done()
	a.log.Info("shutting down")
	return nil
}