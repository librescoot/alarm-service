package redis

import (
	"context"
	"log/slog"

	"alarm-service/internal/fsm"
)

// Subscriber handles subscribing to Redis channels
type Subscriber struct {
	client *Client
	log    *slog.Logger
	sm     *fsm.StateMachine
}

// NewSubscriber creates a new Subscriber
func NewSubscriber(client *Client, sm *fsm.StateMachine, log *slog.Logger) *Subscriber {
	return &Subscriber{
		client: client,
		log:    log,
		sm:     sm,
	}
}

// SubscribeToVehicleState subscribes to vehicle state changes
func (s *Subscriber) SubscribeToVehicleState(ctx context.Context) {
	s.log.Info("subscribing to vehicle state")

	pubsub := s.client.Subscribe(ctx, "vehicle")
	defer pubsub.Close()

	ch := pubsub.Channel()

	for {
		select {
		case <-ctx.Done():
			return

		case msg := <-ch:
			if msg == nil {
				continue
			}

			if msg.Payload == "state" {
				stateStr, err := s.client.HGet(ctx, "vehicle", "state")
				if err != nil {
					s.log.Error("failed to get vehicle state from redis", "error", err)
					continue
				}
				state := fsm.ParseVehicleState(stateStr)
				s.log.Debug("vehicle state changed", "state", state.String())
				s.sm.SendEvent(fsm.VehicleStateChangedEvent{State: state})
			}
		}
	}
}

// SubscribeToAlarmMode subscribes to alarm mode changes
func (s *Subscriber) SubscribeToAlarmMode(ctx context.Context) {
	s.log.Info("subscribing to alarm mode")

	pubsub := s.client.Subscribe(ctx, "settings")
	defer pubsub.Close()

	ch := pubsub.Channel()

	for {
		select {
		case <-ctx.Done():
			return

		case msg := <-ch:
			if msg == nil {
				continue
			}

			if msg.Payload == "alarm-mode" {
				alarmMode, err := s.client.HGet(ctx, "settings", "alarm-mode")
				if err != nil {
					s.log.Error("failed to get alarm mode from redis", "error", err)
					continue
				}
				enabled := alarmMode == "enabled"
				s.log.Debug("alarm mode changed", "enabled", enabled)
				s.sm.SendEvent(fsm.AlarmModeChangedEvent{Enabled: enabled})
			}
		}
	}
}

// SubscribeToBMXInterrupt subscribes to BMX interrupts
func (s *Subscriber) SubscribeToBMXInterrupt(ctx context.Context) {
	s.log.Info("subscribing to BMX interrupts")

	pubsub := s.client.Subscribe(ctx, "bmx:interrupt")
	defer pubsub.Close()

	ch := pubsub.Channel()

	for {
		select {
		case <-ctx.Done():
			return

		case msg := <-ch:
			if msg == nil {
				continue
			}

			s.log.Info("BMX interrupt received")
			s.sm.SendEvent(fsm.BMXInterruptEvent{
				Timestamp: 0,
				Data:      msg.Payload,
			})
		}
	}
}

// CheckInitialState checks initial vehicle and alarm states
func (s *Subscriber) CheckInitialState(ctx context.Context) {
	vehicleState, err := s.client.HGet(ctx, "vehicle", "state")
	if err == nil {
		state := fsm.ParseVehicleState(vehicleState)
		s.log.Info("initial vehicle state", "state", state.String())
		s.sm.SendEvent(fsm.VehicleStateChangedEvent{State: state})
	}

	alarmMode, err := s.client.HGet(ctx, "settings", "alarm-mode")
	if err == nil {
		enabled := alarmMode == "enabled"
		s.log.Info("initial alarm mode", "enabled", enabled)
		s.sm.SendEvent(fsm.AlarmModeChangedEvent{Enabled: enabled})
	}

	bmxInitialized, err := s.client.HGet(ctx, "bmx", "initialized")
	if err == nil && bmxInitialized == "true" {
		s.log.Info("BMX service already initialized")
		s.sm.SendEvent(fsm.InitCompleteEvent{})
	}
}