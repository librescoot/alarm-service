package redis

import (
	"context"
	"fmt"
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

// SubscribeToAlarmSettings subscribes to alarm settings changes
func (s *Subscriber) SubscribeToAlarmSettings(ctx context.Context) {
	s.log.Info("subscribing to alarm settings")

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

			if msg.Payload == "alarm.enabled" {
				alarmEnabled, err := s.client.HGet(ctx, "settings", "alarm.enabled")
				if err != nil {
					s.log.Error("failed to get alarm.enabled from redis", "error", err)
					continue
				}
				enabled := alarmEnabled == "true"
				s.log.Debug("alarm enabled changed", "enabled", enabled)
				s.sm.SendEvent(fsm.AlarmModeChangedEvent{Enabled: enabled})
			}

			if msg.Payload == "alarm.honk" {
				hornEnabled, err := s.client.HGet(ctx, "settings", "alarm.honk")
				if err != nil {
					s.log.Error("failed to get alarm.honk from redis", "error", err)
					continue
				}
				enabled := hornEnabled == "true"
				s.log.Debug("alarm honk changed", "enabled", enabled)
				s.sm.SendEvent(fsm.HornSettingChangedEvent{Enabled: enabled})
			}

			if msg.Payload == "alarm.duration" {
				durationStr, err := s.client.HGet(ctx, "settings", "alarm.duration")
				if err != nil {
					s.log.Error("failed to get alarm.duration from redis", "error", err)
					continue
				}
				var duration int
				if _, err := fmt.Sscanf(durationStr, "%d", &duration); err != nil {
					s.log.Error("invalid alarm.duration value", "value", durationStr, "error", err)
					continue
				}
				s.log.Debug("alarm duration changed", "duration", duration)
				s.sm.SendEvent(fsm.AlarmDurationChangedEvent{Duration: duration})
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

	alarmEnabled, err := s.client.HGet(ctx, "settings", "alarm.enabled")
	if err == nil {
		enabled := alarmEnabled == "true"
		s.log.Info("initial alarm enabled", "enabled", enabled)
		s.sm.SendEvent(fsm.AlarmModeChangedEvent{Enabled: enabled})
	}

	hornEnabled, err := s.client.HGet(ctx, "settings", "alarm.honk")
	if err == nil {
		enabled := hornEnabled == "true"
		s.log.Info("initial horn enabled", "enabled", enabled)
		s.sm.SendEvent(fsm.HornSettingChangedEvent{Enabled: enabled})
	}

	alarmDuration, err := s.client.HGet(ctx, "settings", "alarm.duration")
	if err == nil {
		var duration int
		if _, err := fmt.Sscanf(alarmDuration, "%d", &duration); err == nil {
			s.log.Info("initial alarm duration", "duration", duration)
			s.sm.SendEvent(fsm.AlarmDurationChangedEvent{Duration: duration})
		}
	}

	bmxInitialized, err := s.client.HGet(ctx, "bmx", "initialized")
	if err == nil && bmxInitialized == "true" {
		s.log.Info("BMX service already initialized")
		s.sm.SendEvent(fsm.InitCompleteEvent{})
	}
}