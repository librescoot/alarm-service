package redis

import (
	"fmt"
	"log/slog"

	"alarm-service/internal/fsm"

	ipc "github.com/librescoot/redis-ipc"
)

// Subscriber handles subscribing to Redis channels using HashWatcher
type Subscriber struct {
	vehicleWatcher  *ipc.HashWatcher
	settingsWatcher *ipc.HashWatcher
	bmxWatcher      *ipc.Subscription[string]
	ipc             *ipc.Client
	log             *slog.Logger
	sm              *fsm.StateMachine
}

// NewSubscriber creates a new Subscriber with HashWatcher instances
func NewSubscriber(client *Client, sm *fsm.StateMachine, log *slog.Logger) *Subscriber {
	s := &Subscriber{
		vehicleWatcher:  client.ipc.NewHashWatcher("vehicle"),
		settingsWatcher: client.ipc.NewHashWatcher("settings"),
		ipc:             client.ipc,
		log:             log,
		sm:              sm,
	}

	s.setupVehicleWatcher()
	s.setupSettingsWatcher()

	return s
}

// setupVehicleWatcher registers handlers for vehicle state changes
func (s *Subscriber) setupVehicleWatcher() {
	s.vehicleWatcher.OnField("state", func(stateStr string) error {
		state := fsm.ParseVehicleState(stateStr)
		s.log.Debug("vehicle state changed", "state", state.String())
		s.sm.SendEvent(fsm.VehicleStateChangedEvent{State: state})
		return nil
	})

	s.vehicleWatcher.OnEvent("seatbox:opened", func() error {
		s.log.Info("authorized seatbox opening detected")
		s.sm.SendEvent(fsm.SeatboxOpenedEvent{})
		return nil
	})

	s.vehicleWatcher.OnField("seatbox:lock", func(lockState string) error {
		s.log.Debug("seatbox lock state changed", "state", lockState)
		if lockState == "closed" {
			s.sm.SendEvent(fsm.SeatboxClosedEvent{})
		} else if lockState == "open" {
			currentState := s.sm.State()
			if currentState != fsm.StateSeatboxAccess {
				s.log.Warn("unauthorized seatbox opening detected", "current_state", currentState.String())
				s.sm.SendEvent(fsm.UnauthorizedSeatboxEvent{})
			}
		}
		return nil
	})
}

// setupSettingsWatcher registers handlers for alarm settings changes
func (s *Subscriber) setupSettingsWatcher() {
	s.settingsWatcher.OnField("alarm.enabled", func(alarmEnabled string) error {
		enabled := alarmEnabled == "true"
		s.log.Debug("alarm enabled changed", "enabled", enabled)

		if enabled {
			vehicleState, err := s.vehicleWatcher.Fetch("state")
			if err == nil {
				state := fsm.ParseVehicleState(vehicleState)
				s.log.Debug("sending current vehicle state before alarm enable", "state", state.String())
				s.sm.SendEvent(fsm.VehicleStateChangedEvent{State: state})
			}
		}

		s.sm.SendEvent(fsm.AlarmModeChangedEvent{Enabled: enabled})
		return nil
	})

	s.settingsWatcher.OnField("alarm.honk", func(hornEnabled string) error {
		enabled := hornEnabled == "true"
		s.log.Debug("alarm honk changed", "enabled", enabled)
		s.sm.SendEvent(fsm.HornSettingChangedEvent{Enabled: enabled})
		return nil
	})

	s.settingsWatcher.OnField("alarm.duration", func(durationStr string) error {
		var duration int
		if _, err := fmt.Sscanf(durationStr, "%d", &duration); err != nil {
			s.log.Error("invalid alarm.duration value", "value", durationStr, "error", err)
			return nil
		}
		s.log.Debug("alarm duration changed", "duration", duration)
		s.sm.SendEvent(fsm.AlarmDurationChangedEvent{Duration: duration})
		return nil
	})
}

// Start starts all watchers with initial state sync
func (s *Subscriber) Start() error {
	s.log.Info("starting hash watchers with initial sync")

	if err := s.vehicleWatcher.StartWithSync(); err != nil {
		return fmt.Errorf("failed to start vehicle watcher: %w", err)
	}

	if err := s.settingsWatcher.StartWithSync(); err != nil {
		return fmt.Errorf("failed to start settings watcher: %w", err)
	}

	s.log.Info("starting BMX interrupt subscription")
	var err error
	s.bmxWatcher, err = ipc.Subscribe(s.ipc, "bmx:interrupt", func(payload string) error {
		s.log.Info("BMX interrupt received")
		s.sm.SendEvent(fsm.BMXInterruptEvent{
			Timestamp: 0,
			Data:      payload,
		})
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to subscribe to bmx:interrupt: %w", err)
	}

	return nil
}

// CheckBMXInitialized checks if BMX service is already initialized
func (s *Subscriber) CheckBMXInitialized() error {
	bmxInitialized, err := s.ipc.HGet("bmx", "initialized")
	if err == nil && bmxInitialized == "true" {
		s.log.Info("BMX service already initialized")
		s.sm.SendEvent(fsm.InitCompleteEvent{})
	}
	return nil
}

// Stop stops all watchers
func (s *Subscriber) Stop() {
	s.vehicleWatcher.Stop()
	s.settingsWatcher.Stop()
	if s.bmxWatcher != nil {
		s.bmxWatcher.Unsubscribe()
	}
}
