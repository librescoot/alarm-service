package redis

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"alarm-service/internal/fsm"

	ipc "github.com/librescoot/redis-ipc"
)

// seatboxBounceWindow filters spurious sensor edges right after an authorized
// close. The latch can rebound briefly while the lid settles, which used to
// surface as seatbox:lock=open without a paired seatbox:opened event and
// triggered StateTriggerLevel2. 500ms covers observed mechanical settle time
// while staying well below any plausible legit re-open cadence. A legit
// re-open within this window still works because the app/button path emits
// seatbox:opened first, which sets authorizedSeatboxPending and bypasses the
// bounce check entirely.
const seatboxBounceWindow = 500 * time.Millisecond

// motionEvent mirrors motion-service's MotionEvent JSON envelope. Kept
// minimal to avoid a hard dependency on the motion-service repo. The Type
// field is what gets propagated into BMXInterruptEvent.Data — the FSM
// already discriminates "wake-hibernation" from regular edges via that
// field. Decoded by hand in the subscription handler so this works
// alongside the alarm-service redis-ipc client's StringCodec default.
type motionEvent struct {
	Type      string `json:"type"`
	Timestamp int64  `json:"timestamp"`
	Engine    string `json:"engine,omitempty"`
}

// eventSink is the part of fsm.StateMachine the subscriber uses. Extracted so
// the tamper-input handlers can be exercised without a running FSM.
type eventSink interface {
	SendEvent(fsm.Event)
	State() fsm.State
}

// Subscriber handles subscribing to Redis channels using HashWatcher
type Subscriber struct {
	vehicleWatcher           *ipc.HashWatcher
	settingsWatcher          *ipc.HashWatcher
	powerManagerWatcher      *ipc.HashWatcher
	motionWatcher            *ipc.Subscription[string]
	buttonsWatcher           *ipc.Subscription[string]
	ipc                      *ipc.Client
	log                      *slog.Logger
	sm                       eventSink
	authorizedSeatboxPending bool
	lastSeatboxCloseAt       time.Time

	// Per-source trigger flags. Rejected events are dropped here rather than
	// in the FSM, so an opted-out source costs nothing downstream. Atomic
	// because the settings watcher writes them from its own goroutine while
	// the vehicle watcher and the buttons subscription read them from theirs.
	seatboxTriggerEnabled   atomic.Bool
	buttonsTriggerEnabled   atomic.Bool
	handlebarTriggerEnabled atomic.Bool

	// Last seen values of the handlebar tamper fields, used to tell a real
	// safe-to-unsafe transition from StartWithSync delivering the value that
	// was already there. Plenty of scooters park with the handlebar lock never
	// engaged, so "unlocked" is a legitimate resting value and must not fire
	// the alarm on every service restart.
	handlebarLockLast     string
	handlebarPositionLast string
}

// NewSubscriber creates a new Subscriber with HashWatcher instances
func NewSubscriber(client *Client, sm *fsm.StateMachine, log *slog.Logger) *Subscriber {
	s := &Subscriber{
		vehicleWatcher:      client.ipc.NewHashWatcher("vehicle"),
		settingsWatcher:     client.ipc.NewHashWatcher("settings"),
		powerManagerWatcher: client.ipc.NewHashWatcher("power-manager"),
		ipc:                 client.ipc,
		log:                 log,
		sm:                  sm,
	}

	// default: seatbox, brake/horn/seatbox buttons and handlebar sensors can
	// all trigger the alarm
	s.seatboxTriggerEnabled.Store(true)
	s.buttonsTriggerEnabled.Store(true)
	s.handlebarTriggerEnabled.Store(true)

	s.setupVehicleWatcher()
	s.setupSettingsWatcher()
	s.setupPowerManagerWatcher()

	return s
}

// isHibernatingImminentState reports whether a power-manager state value indicates
// that hibernation is imminent or in progress. Suspend is intentionally excluded —
// the BMX hibernation profile is only meant to gate full power-down events.
func isHibernatingImminentState(state string) bool {
	switch state {
	case "hibernating-imminent",
		"hibernating-manual-imminent",
		"hibernating-timer-imminent",
		"hibernating",
		"hibernating-manual",
		"hibernating-timer":
		return true
	}
	return false
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

	s.vehicleWatcher.OnField("seatbox:lock", s.handleSeatboxLockField)
	s.vehicleWatcher.OnField("handlebar:lock-sensor", s.handleHandlebarLockField)
	s.vehicleWatcher.OnField("handlebar:position", s.handleHandlebarPositionField)
}

// handleSeatboxLockField turns a seatbox latch edge into either an authorized
// opening or a tamper trigger, after filtering the sensor bounce that follows
// an authorized close.
func (s *Subscriber) handleSeatboxLockField(lockState string) error {
	s.log.Debug("seatbox lock state changed", "state", lockState)
	if lockState == "closed" {
		s.authorizedSeatboxPending = false
		s.lastSeatboxCloseAt = time.Now()
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
		if since := time.Since(s.lastSeatboxCloseAt); since < seatboxBounceWindow {
			s.log.Info("seatbox open ignored as sensor bounce",
				"since_close_ms", since.Milliseconds(),
				"current_state", currentState.String())
			return nil
		}
		if !s.seatboxTriggerEnabled.Load() {
			s.log.Info("seatbox opened, treating as authorized (seatbox-trigger disabled)")
			s.sm.SendEvent(fsm.SeatboxOpenedEvent{})
		} else {
			s.log.Warn("unauthorized seatbox opening detected", "current_state", currentState.String())
			s.sm.SendEvent(fsm.UnauthorizedSeatboxEvent{})
		}
	}
	return nil
}

// handleHandlebarLockField emits a trigger only for a locked-to-unlocked
// transition seen after the baseline value has been captured. See the
// handlebarLockLast comment for why the baseline matters.
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
	if !s.handlebarTriggerEnabled.Load() {
		s.log.Debug("handlebar unlocked transition ignored, handlebar trigger disabled")
		return nil
	}
	s.log.Info("handlebar lock went unlocked, sending input trigger", "prev", prev)
	s.sm.SendEvent(fsm.InputTriggerEvent{Source: fsm.TriggerSourceHandlebarLock})
	return nil
}

// handleHandlebarPositionField is the position-sensor counterpart of
// handleHandlebarLockField. Only on-place to off-place counts, and only after
// the baseline: a rider who parked with the bars turned leaves "off-place" as
// the resting value.
func (s *Subscriber) handleHandlebarPositionField(position string) error {
	prev := s.handlebarPositionLast
	s.handlebarPositionLast = position
	if prev == "" {
		s.log.Debug("handlebar position baseline captured", "position", position)
		return nil
	}
	if position != "off-place" || prev == "off-place" {
		return nil
	}
	if !s.handlebarTriggerEnabled.Load() {
		s.log.Debug("handlebar off-place transition ignored, handlebar trigger disabled")
		return nil
	}
	s.log.Info("handlebar moved off-place, sending input trigger", "prev", prev)
	s.sm.SendEvent(fsm.InputTriggerEvent{Source: fsm.TriggerSourceHandlebarPosition})
	return nil
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
		s.seatboxTriggerEnabled.Store(enabled)
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

	s.settingsWatcher.OnField("alarm.trigger.motion", func(motionTrigger string) error {
		enabled := motionTrigger == "true"
		s.log.Info("trigger.motion setting changed", "enabled", enabled)
		s.sm.SendEvent(fsm.MotionTriggerSettingChangedEvent{Enabled: enabled})
		return nil
	})

	s.settingsWatcher.OnField("alarm.trigger.buttons", func(buttonsTrigger string) error {
		enabled := buttonsTrigger == "true"
		s.log.Info("trigger.buttons setting changed", "enabled", enabled)
		s.buttonsTriggerEnabled.Store(enabled)
		return nil
	})

	s.settingsWatcher.OnField("alarm.trigger.handlebar", func(handlebarTrigger string) error {
		enabled := handlebarTrigger == "true"
		s.log.Info("trigger.handlebar setting changed", "enabled", enabled)
		s.handlebarTriggerEnabled.Store(enabled)
		return nil
	})
}

// setupPowerManagerWatcher reacts to pm-service publishing its current power-manager
// state. The hibernation-imminent phase (and the hibernation phase itself, in case we
// race the transition) flips the alarm into the stricter armed-state profile.
func (s *Subscriber) setupPowerManagerWatcher() {
	s.powerManagerWatcher.OnField("state", func(stateStr string) error {
		imminent := isHibernatingImminentState(stateStr)
		s.log.Debug("power-manager state changed", "state", stateStr, "hibernation_imminent", imminent)
		s.sm.SendEvent(fsm.HibernationImminentEvent{Imminent: imminent})
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

	if err := s.powerManagerWatcher.StartWithSync(); err != nil {
		return fmt.Errorf("failed to start power-manager watcher: %w", err)
	}

	s.sm.SendEvent(fsm.InitCompleteEvent{})

	s.log.Info("subscribing to motion:interrupt")
	var err error
	s.motionWatcher, err = ipc.Subscribe(s.ipc, "motion:interrupt", func(payload string) error {
		var evt motionEvent
		if err := json.Unmarshal([]byte(payload), &evt); err != nil {
			s.log.Warn("malformed motion:interrupt payload", "payload", payload, "error", err)
			return nil
		}
		s.log.Info("motion event received", "type", evt.Type, "engine", evt.Engine, "timestamp", evt.Timestamp)
		s.sm.SendEvent(fsm.BMXInterruptEvent{
			Timestamp: evt.Timestamp,
			Data:      evt.Type,
		})
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to subscribe to motion:interrupt: %w", err)
	}

	s.log.Info("subscribing to buttons")
	s.buttonsWatcher, err = ipc.Subscribe(s.ipc, "buttons", s.handleButtonEvent)
	if err != nil {
		return fmt.Errorf("failed to subscribe to buttons: %w", err)
	}

	return nil
}

// handleButtonEvent turns a `buttons` payload into a tamper trigger. Only the
// pressed edge counts; releases are ignored so a single press produces one
// trigger.
func (s *Subscriber) handleButtonEvent(payload string) error {
	source, edge, ok := parseButtonPayload(payload)
	if !ok {
		s.log.Debug("unrecognized button payload", "payload", payload)
		return nil
	}
	if edge != "on" {
		return nil
	}
	if !s.buttonsTriggerEnabled.Load() {
		s.log.Debug("button press ignored, buttons trigger disabled", "source", source.String())
		return nil
	}
	s.log.Info("button pressed, sending input trigger", "source", source.String())
	s.sm.SendEvent(fsm.InputTriggerEvent{Source: source})
	return nil
}

// parseButtonPayload recognizes the `buttons` payloads that count as tampering.
// vehicle-service publishes "horn:on", "seatbox:on", "brake:left:on" and their
// off counterparts on this channel, plus blinker edges. Blinkers are navigation
// signals rather than tampering, so they fall through as unrecognized.
//
// Throttle never appears here. It only exists as an ECU CAN payload and the ECU
// is powered down in Standby, so it cannot be a trigger source.
func parseButtonPayload(payload string) (fsm.TriggerSource, string, bool) {
	parts := strings.Split(payload, ":")
	switch len(parts) {
	case 2:
		edge := parts[1]
		switch parts[0] {
		case "seatbox":
			return fsm.TriggerSourceSeatboxButton, edge, true
		case "horn":
			return fsm.TriggerSourceHornButton, edge, true
		}
	case 3:
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
	if err := s.vehicleWatcher.Stop(); err != nil {
		s.log.Warn("failed to stop vehicle watcher", "error", err)
	}
	if err := s.settingsWatcher.Stop(); err != nil {
		s.log.Warn("failed to stop settings watcher", "error", err)
	}
	if err := s.powerManagerWatcher.Stop(); err != nil {
		s.log.Warn("failed to stop power-manager watcher", "error", err)
	}
	if s.motionWatcher != nil {
		if err := s.motionWatcher.Unsubscribe(); err != nil {
			s.log.Warn("failed to unsubscribe motion watcher", "error", err)
		}
	}
	if s.buttonsWatcher != nil {
		if err := s.buttonsWatcher.Unsubscribe(); err != nil {
			s.log.Warn("failed to unsubscribe buttons watcher", "error", err)
		}
	}
}
