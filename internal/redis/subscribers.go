package redis

import (
	"fmt"
	"log/slog"

	"alarm-service/internal/fsm"

	ipc "github.com/librescoot/redis-ipc"
)

// Subscriber handles subscribing to Redis channels using HashWatcher
type Subscriber struct {
	vehicleWatcher        *ipc.HashWatcher
	settingsWatcher       *ipc.HashWatcher
	bmxWatcher            *ipc.Subscription[string]
	ipc                   *ipc.Client
	log                   *slog.Logger
	sm                    *fsm.StateMachine
	seatboxTriggerEnabled bool
}

// NewSubscriber creates a new Subscriber with HashWatcher instances
func NewSubscriber(client *Client, sm *fsm.StateMachine, log *slog.Logger) *Subscriber {
	s := &Subscriber{
		vehicleWatcher:        client.ipc.NewHashWatcher("vehicle"),
		settingsWatcher:       client.ipc.NewHashWatcher("settings"),
		ipc:                   client.ipc,
		log:                   log,
		sm:                    sm,
		seatboxTriggerEnabled: true,
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
		s.sm.SendEvent(fsm.NewVehicleStateChangedEvent(state))
		return nil
	})

	s.vehicleWatcher.OnEvent("seatbox:opened", func() error {
		s.log.Info("authorized seatbox opening detected")
		s.sm.SendEvent(fsm.NewSeatboxOpenedEvent())
		return nil
	})

	s.vehicleWatcher.OnField("seatbox:lock", func(lockState string) error {
		s.log.Debug("seatbox lock state changed", "state", lockState)
		switch lockState {
		case "closed":
			s.sm.SendEvent(fsm.NewSeatboxClosedEvent())
		case "open":
			currentState := s.sm.State()
			if currentState == fsm.StateSeatboxAccess {
				return nil
			}
			if !s.seatboxTriggerEnabled {
				s.log.Info("seatbox opened, treating as authorized (seatbox-trigger disabled)")
				s.sm.SendEvent(fsm.NewSeatboxOpenedEvent())
			} else {
				s.log.Warn("unauthorized seatbox opening detected", "current_state", string(currentState))
				s.sm.SendEvent(fsm.NewUnauthorizedSeatboxEvent())
			}
		}
		return nil
	})
}

// setupSettingsWatcher registers handlers for alarm settings changes.
// State-changing settings (alarm.enabled) go through the FSM.
// Tuning parameters are applied directly via thread-safe setters.
func (s *Subscriber) setupSettingsWatcher() {
	s.settingsWatcher.OnField("alarm.enabled", func(alarmEnabled string) error {
		enabled := alarmEnabled == "true"
		s.log.Debug("alarm enabled changed", "enabled", enabled)

		if enabled {
			vehicleState, err := s.vehicleWatcher.Fetch("state")
			if err == nil {
				state := fsm.ParseVehicleState(vehicleState)
				s.log.Debug("sending current vehicle state before alarm enable", "state", state.String())
				s.sm.SendEvent(fsm.NewVehicleStateChangedEvent(state))
			}
		}

		s.sm.SendEvent(fsm.NewAlarmModeChangedEvent(enabled))
		return nil
	})

	s.settingsWatcher.OnField("alarm.honk", func(hornEnabled string) error {
		enabled := hornEnabled == "true"
		s.log.Debug("alarm honk changed", "enabled", enabled)
		s.sm.SetHornEnabled(enabled)
		return nil
	})

	s.settingsWatcher.OnField("alarm.duration", func(durationStr string) error {
		var duration int
		if _, err := fmt.Sscanf(durationStr, "%d", &duration); err != nil {
			s.log.Error("invalid alarm.duration value", "value", durationStr, "error", err)
			return nil
		}
		s.sm.SetAlarmDuration(duration)
		return nil
	})

	s.settingsWatcher.OnField("alarm.seatbox-trigger", func(seatboxTrigger string) error {
		enabled := seatboxTrigger == "true"
		s.log.Info("seatbox-trigger setting changed", "enabled", enabled)
		s.seatboxTriggerEnabled = enabled
		return nil
	})

	s.settingsWatcher.OnField("alarm.hairtrigger", func(hairTrigger string) error {
		enabled := hairTrigger == "true"
		s.log.Debug("hair trigger setting changed", "enabled", enabled)
		s.sm.SetHairTriggerEnabled(enabled)
		return nil
	})

	s.settingsWatcher.OnField("alarm.hairtrigger-duration", func(durationStr string) error {
		var duration int
		if _, err := fmt.Sscanf(durationStr, "%d", &duration); err != nil {
			s.log.Error("invalid alarm.hairtrigger-duration value", "value", durationStr, "error", err)
			return nil
		}
		s.sm.SetHairTriggerDuration(duration)
		return nil
	})

	s.settingsWatcher.OnField("alarm.l1-cooldown", func(durationStr string) error {
		var duration int
		if _, err := fmt.Sscanf(durationStr, "%d", &duration); err != nil {
			s.log.Error("invalid alarm.l1-cooldown value", "value", durationStr, "error", err)
			return nil
		}
		s.sm.SetL1CooldownDuration(duration)
		return nil
	})
}

// Start starts all watchers with initial state sync and signals the FSM to
// leave StateInit.
func (s *Subscriber) Start() error {
	s.log.Info("starting hash watchers with initial sync")

	if err := s.vehicleWatcher.StartWithSync(); err != nil {
		return fmt.Errorf("failed to start vehicle watcher: %w", err)
	}

	if err := s.settingsWatcher.StartWithSync(); err != nil {
		return fmt.Errorf("failed to start settings watcher: %w", err)
	}

	s.sm.SendEvent(fsm.NewInitCompleteEvent())

	s.log.Info("starting BMX interrupt subscription")
	var err error
	s.bmxWatcher, err = ipc.Subscribe(s.ipc, "bmx:interrupt", func(payload string) error {
		s.log.Info("BMX interrupt received", "payload", payload)
		s.sm.SendEvent(fsm.NewBMXInterruptEvent(payload))
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to subscribe to bmx:interrupt: %w", err)
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
