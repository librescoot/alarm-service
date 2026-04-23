package redis

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"alarm-service/internal/fsm"

	ipc "github.com/librescoot/redis-ipc"
)

// eventSink is the subset of fsm.StateMachine the subscriber needs. Extracted
// so unit tests can verify event dispatch without constructing a full FSM.
type eventSink interface {
	SendEvent(fsm.Event)
	State() fsm.State
}

// Subscriber handles subscribing to Redis channels using HashWatcher
type Subscriber struct {
	vehicleWatcher          *ipc.HashWatcher
	settingsWatcher         *ipc.HashWatcher
	bmxWatcher              *ipc.Subscription[string]
	buttonsWatcher          *ipc.Subscription[string]
	ipc                     *ipc.Client
	log                     *slog.Logger
	sm                      eventSink
	seatboxTriggerEnabled   bool
	authorizedSeatboxPending bool

	// Per-source trigger enable flags. Mirror the FSM's own copy so the
	// subscriber can drop events at the source without waking the FSM, and
	// notify the FSM for its own bookkeeping on change. Default true; users
	// opt *out* of a source to get the "inputs-only" or "motion-only" preset.
	flagMu           sync.RWMutex
	buttonsEnabled   bool
	handlebarEnabled bool

	// Previous values for hash fields that represent tamper state. Used to
	// distinguish genuine safe->unsafe transitions from StartWithSync's
	// initial-value delivery. Many scooters park with the handlebar lock
	// never engaged, so "unlocked" is a legitimate resting value we must
	// not treat as a trigger on service startup.
	handlebarLockLast string
	handlebarPosLast  string
}

// NewSubscriber creates a new Subscriber with HashWatcher instances
func NewSubscriber(client *Client, sm *fsm.StateMachine, log *slog.Logger) *Subscriber {
	s := &Subscriber{
		vehicleWatcher:        client.ipc.NewHashWatcher("vehicle"),
		settingsWatcher:       client.ipc.NewHashWatcher("settings"),
		ipc:                   client.ipc,
		log:                   log,
		sm:                    sm,
		seatboxTriggerEnabled: true, // default: seatbox opening can trigger alarm
		buttonsEnabled:        true, // default: brake/horn/seatbox buttons can trigger alarm
		handlebarEnabled:      true, // default: handlebar lock/position can trigger alarm
	}

	s.setupVehicleWatcher()
	s.setupSettingsWatcher()

	return s
}

func (s *Subscriber) getButtonsEnabled() bool {
	s.flagMu.RLock()
	defer s.flagMu.RUnlock()
	return s.buttonsEnabled
}

func (s *Subscriber) getHandlebarEnabled() bool {
	s.flagMu.RLock()
	defer s.flagMu.RUnlock()
	return s.handlebarEnabled
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
		s.authorizedSeatboxPending = true
		s.sm.SendEvent(fsm.SeatboxOpenedEvent{})
		return nil
	})

	s.vehicleWatcher.OnField("seatbox:lock", func(lockState string) error {
		s.log.Debug("seatbox lock state changed", "state", lockState)
		if lockState == "closed" {
			s.authorizedSeatboxPending = false
			s.sm.SendEvent(fsm.SeatboxClosedEvent{})
		} else if lockState == "open" {
			if s.authorizedSeatboxPending {
				// seatbox:opened event was already received for this opening cycle; skip
				return nil
			}
			currentState := s.sm.State()
			if currentState == fsm.StateSeatboxAccess {
				return nil
			}
			if !s.seatboxTriggerEnabled {
				s.log.Info("seatbox opened, treating as authorized (seatbox-trigger disabled)")
				s.sm.SendEvent(fsm.SeatboxOpenedEvent{})
			} else {
				s.log.Warn("unauthorized seatbox opening detected", "current_state", currentState.String())
				s.sm.SendEvent(fsm.UnauthorizedSeatboxEvent{})
			}
		}
		return nil
	})

	// Handlebar lock sensor. See handleHandlebarLockField for semantics.
	s.vehicleWatcher.OnField("handlebar:lock-sensor", s.handleHandlebarLockField)

	// Handlebar position sensor. See handleHandlebarPositionField for semantics.
	s.vehicleWatcher.OnField("handlebar:position", s.handleHandlebarPositionField)
}

// handleHandlebarLockField emits an InputTriggerEvent only on a genuine
// safe->unsafe transition observed after the initial sync. Scooters routinely
// park with the handlebar lock never engaged, so "unlocked" is often the
// resting value. Triggering on StartWithSync's initial delivery would fire
// the alarm on every service restart of an already-armed scooter.
func (s *Subscriber) handleHandlebarLockField(lockState string) error {
	prev := s.handlebarLockLast
	s.handlebarLockLast = lockState
	if prev == "" {
		s.log.Debug("handlebar lock baseline captured", "state", lockState)
		return nil
	}
	if lockState != "unlocked" || prev == "unlocked" {
		return nil
	}
	if !s.getHandlebarEnabled() {
		s.log.Debug("handlebar unlocked transition ignored (handlebar trigger disabled)")
		return nil
	}
	s.log.Info("handlebar lock transition to unlocked, sending input trigger", "prev", prev)
	s.sm.SendEvent(fsm.InputTriggerEvent{Source: fsm.TriggerSourceHandlebarLock})
	return nil
}

// handleHandlebarPositionField is the position-sensor counterpart. Same
// rationale: if the rider parked without turning the bars, "off-place" is
// the resting value and must not trigger on startup. Only on-place to
// off-place transitions count.
func (s *Subscriber) handleHandlebarPositionField(pos string) error {
	prev := s.handlebarPosLast
	s.handlebarPosLast = pos
	if prev == "" {
		s.log.Debug("handlebar position baseline captured", "position", pos)
		return nil
	}
	if pos != "off-place" || prev == "off-place" {
		return nil
	}
	if !s.getHandlebarEnabled() {
		s.log.Debug("handlebar off-place transition ignored (handlebar trigger disabled)")
		return nil
	}
	s.log.Info("handlebar position transition to off-place, sending input trigger", "prev", prev)
	s.sm.SendEvent(fsm.InputTriggerEvent{Source: fsm.TriggerSourceHandlebarPosition})
	return nil
}

// parseBoolSetting accepts the common Redis-flavored truthy strings.
func parseBoolSetting(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "1", "yes", "on":
		return true
	}
	return false
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

	s.settingsWatcher.OnField("alarm.seatbox-trigger", func(seatboxTrigger string) error {
		enabled := seatboxTrigger == "true"
		s.log.Info("seatbox-trigger setting changed", "enabled", enabled)
		s.seatboxTriggerEnabled = enabled
		return nil
	})

	s.settingsWatcher.OnField("alarm.hairtrigger", func(hairTrigger string) error {
		enabled := hairTrigger == "true"
		s.log.Debug("hair trigger setting changed", "enabled", enabled)
		s.sm.SendEvent(fsm.HairTriggerSettingChangedEvent{Enabled: enabled})
		return nil
	})

	s.settingsWatcher.OnField("alarm.hairtrigger-duration", func(durationStr string) error {
		var duration int
		if _, err := fmt.Sscanf(durationStr, "%d", &duration); err != nil {
			s.log.Error("invalid alarm.hairtrigger-duration value", "value", durationStr, "error", err)
			return nil
		}
		s.log.Debug("hair trigger duration changed", "duration", duration)
		s.sm.SendEvent(fsm.HairTriggerDurationChangedEvent{Duration: duration})
		return nil
	})

	s.settingsWatcher.OnField("alarm.l1-cooldown", func(durationStr string) error {
		var duration int
		if _, err := fmt.Sscanf(durationStr, "%d", &duration); err != nil {
			s.log.Error("invalid alarm.l1-cooldown value", "value", durationStr, "error", err)
			return nil
		}
		s.log.Debug("L1 cooldown duration changed", "duration", duration)
		s.sm.SendEvent(fsm.L1CooldownDurationChangedEvent{Duration: duration})
		return nil
	})

	s.settingsWatcher.OnField("alarm.trigger.motion", func(v string) error {
		enabled := parseBoolSetting(v)
		s.log.Info("trigger.motion setting changed", "enabled", enabled)
		s.sm.SendEvent(fsm.TriggerSourceSettingChangedEvent{
			Category: fsm.TriggerCategoryMotion,
			Enabled:  enabled,
		})
		return nil
	})

	s.settingsWatcher.OnField("alarm.trigger.buttons", func(v string) error {
		enabled := parseBoolSetting(v)
		s.log.Info("trigger.buttons setting changed", "enabled", enabled)
		s.flagMu.Lock()
		s.buttonsEnabled = enabled
		s.flagMu.Unlock()
		s.sm.SendEvent(fsm.TriggerSourceSettingChangedEvent{
			Category: fsm.TriggerCategoryButtons,
			Enabled:  enabled,
		})
		return nil
	})

	s.settingsWatcher.OnField("alarm.trigger.handlebar", func(v string) error {
		enabled := parseBoolSetting(v)
		s.log.Info("trigger.handlebar setting changed", "enabled", enabled)
		s.flagMu.Lock()
		s.handlebarEnabled = enabled
		s.flagMu.Unlock()
		s.sm.SendEvent(fsm.TriggerSourceSettingChangedEvent{
			Category: fsm.TriggerCategoryHandlebar,
			Enabled:  enabled,
		})
		return nil
	})

	s.settingsWatcher.OnField("alarm.sensitivity", func(v string) error {
		sensitivity := fsm.ParseSensitivity(strings.TrimSpace(v))
		s.log.Info("sensitivity setting changed", "sensitivity", sensitivity.String())
		s.sm.SendEvent(fsm.SensitivityChangedEvent{Sensitivity: sensitivity})
		return nil
	})
}

// Start starts all watchers with initial state sync and signals the FSM to
// leave StateInit. StartWithSync delivers current field values via OnField
// callbacks before returning, so the FSM receives AlarmModeChangedEvent and
// VehicleStateChangedEvent before InitCompleteEvent — no separate read needed.
func (s *Subscriber) Start() error {
	s.log.Info("starting hash watchers with initial sync")

	if err := s.vehicleWatcher.StartWithSync(); err != nil {
		return fmt.Errorf("failed to start vehicle watcher: %w", err)
	}

	if err := s.settingsWatcher.StartWithSync(); err != nil {
		return fmt.Errorf("failed to start settings watcher: %w", err)
	}

	s.sm.SendEvent(fsm.InitCompleteEvent{})

	s.log.Info("starting BMX interrupt subscription")
	var err error
	s.bmxWatcher, err = ipc.Subscribe(s.ipc, "bmx:interrupt", func(payload string) error {
		s.log.Info("BMX interrupt received", "payload", payload)
		s.sm.SendEvent(fsm.BMXInterruptEvent{
			Timestamp: 0,
			Data:      payload,
		})
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to subscribe to bmx:interrupt: %w", err)
	}

	s.log.Info("starting buttons subscription")
	s.buttonsWatcher, err = ipc.Subscribe(s.ipc, "buttons", s.handleButtonEvent)
	if err != nil {
		return fmt.Errorf("failed to subscribe to buttons: %w", err)
	}

	return nil
}

// handleButtonEvent routes a `buttons` PUBSUB payload to the FSM as an
// InputTriggerEvent when the source is armed for triggering and the edge is
// "on". Payload format mirrors vehicle-service's PublishButtonEvent output,
// e.g. "brake:left:on", "brake:right:off", "seatbox:on", "horn:on".
func (s *Subscriber) handleButtonEvent(payload string) error {
	source, edge, ok := parseButtonPayload(payload)
	if !ok {
		s.log.Debug("unrecognized button payload", "payload", payload)
		return nil
	}
	if edge != "on" {
		return nil
	}
	if !s.getButtonsEnabled() {
		s.log.Debug("button press ignored (buttons trigger disabled)", "source", source.String())
		return nil
	}
	s.log.Info("button press, sending input trigger", "source", source.String())
	s.sm.SendEvent(fsm.InputTriggerEvent{Source: source})
	return nil
}

// parseButtonPayload recognizes the subset of `buttons` channel payloads we
// want to act on as tamper triggers. Blinker and throttle are ignored:
// blinkers are ambient navigation signals, and throttle is not exposed
// while the ECU is off.
func parseButtonPayload(payload string) (fsm.TriggerSource, string, bool) {
	parts := strings.Split(payload, ":")
	switch len(parts) {
	case 2:
		// "seatbox:on", "horn:on"
		edge := parts[1]
		switch parts[0] {
		case "seatbox":
			return fsm.TriggerSourceSeatboxButton, edge, true
		case "horn":
			return fsm.TriggerSourceHornButton, edge, true
		}
	case 3:
		// "brake:left:on", "brake:right:on"
		if parts[0] == "brake" {
			edge := parts[2]
			switch parts[1] {
			case "left":
				return fsm.TriggerSourceBrakeLeft, edge, true
			case "right":
				return fsm.TriggerSourceBrakeRight, edge, true
			}
		}
	}
	return fsm.TriggerSourceUnknown, "", false
}

// Stop stops all watchers
func (s *Subscriber) Stop() {
	s.vehicleWatcher.Stop()
	s.settingsWatcher.Stop()
	if s.bmxWatcher != nil {
		s.bmxWatcher.Unsubscribe()
	}
	if s.buttonsWatcher != nil {
		s.buttonsWatcher.Unsubscribe()
	}
}
