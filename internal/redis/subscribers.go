package redis

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"alarm-service/internal/fsm"

	ipc "github.com/librescoot/redis-ipc"
)

// Ignore latch rebound after an authorized close; a real reopen first emits
// seatbox:opened and therefore bypasses this filter.
const seatboxBounceWindow = 500 * time.Millisecond

// Position must remain unsafe long enough to distinguish tampering from wind
// or sensor alignment noise; the lock reading is not trusted as a gate.
const defaultHandlebarPositionDwell = 1 * time.Second

type motionEvent struct {
	Type      string `json:"type"`
	Timestamp int64  `json:"timestamp"`
	Engine    string `json:"engine,omitempty"`
}

type eventSink interface {
	SendEvent(fsm.Event)
	State() fsm.State
}

type Subscriber struct {
	vehicleWatcher           *ipc.HashWatcher
	settingsWatcher          *ipc.HashWatcher
	powerManagerWatcher      *ipc.HashWatcher
	usbWatcher               *ipc.HashWatcher
	motionWatcher            *ipc.Subscription[string]
	buttonsWatcher           *ipc.Subscription[string]
	ipc                      *ipc.Client
	log                      *slog.Logger
	sm                       eventSink
	authorizedSeatboxPending bool
	lastSeatboxCloseAt       time.Time

	seatboxTriggerEnabled   atomic.Bool
	buttonsTriggerEnabled   atomic.Bool
	handlebarTriggerEnabled atomic.Bool

	// Initial StartWithSync values establish a baseline; parked scooters may
	// legitimately start with either sensor unsafe.
	handlebarLockLast     string
	handlebarPositionLast string

	// The generation invalidates a timer that has fired but is waiting on mu.
	handlebarDwellMu       sync.Mutex
	handlebarDwellTimer    *time.Timer
	handlebarDwellGen      uint64
	handlebarOffPlaceSince time.Time
	handlebarPositionDwell time.Duration
}

func NewSubscriber(client *Client, sm *fsm.StateMachine, log *slog.Logger) *Subscriber {
	s := &Subscriber{
		vehicleWatcher:      client.ipc.NewHashWatcher("vehicle"),
		settingsWatcher:     client.ipc.NewHashWatcher("settings"),
		powerManagerWatcher: client.ipc.NewHashWatcher("power-manager"),
		usbWatcher:          client.ipc.NewHashWatcher("usb"),
		ipc:                 client.ipc,
		log:                 log,
		sm:                  sm,
	}

	s.seatboxTriggerEnabled.Store(true)
	s.buttonsTriggerEnabled.Store(true)
	s.handlebarTriggerEnabled.Store(false)
	s.handlebarPositionDwell = defaultHandlebarPositionDwell

	s.setupVehicleWatcher()
	s.setupSettingsWatcher()
	s.setupPowerManagerWatcher()
	s.setupUSBWatcher()

	return s
}

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

func (s *Subscriber) handleSeatboxLockField(lockState string) error {
	s.log.Debug("seatbox lock state changed", "state", lockState)
	if lockState == "closed" {
		s.authorizedSeatboxPending = false
		s.lastSeatboxCloseAt = time.Now()
		s.sm.SendEvent(fsm.SeatboxClosedEvent{})
	} else if lockState == "open" {
		if s.authorizedSeatboxPending {

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

// Only a post-baseline locked-to-unlocked transition is tampering.
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

func (s *Subscriber) handleHandlebarPositionField(position string) error {
	prev := s.handlebarPositionLast
	s.handlebarPositionLast = position
	if prev == "" {
		s.log.Debug("handlebar position baseline captured", "position", position)
		return nil
	}
	if position != "off-place" {

		s.cancelHandlebarDwell()
		return nil
	}
	if prev == "off-place" {
		return nil
	}
	if !s.handlebarTriggerEnabled.Load() {
		s.log.Debug("handlebar off-place transition ignored, handlebar trigger disabled")
		return nil
	}
	s.startHandlebarDwell(prev)
	return nil
}

// Restart on each edge so chattering cannot accumulate a full dwell period.
func (s *Subscriber) startHandlebarDwell(prev string) {
	s.handlebarDwellMu.Lock()
	defer s.handlebarDwellMu.Unlock()

	if s.handlebarDwellTimer != nil {
		s.handlebarDwellTimer.Stop()
	}
	s.handlebarDwellGen++
	gen := s.handlebarDwellGen
	s.handlebarOffPlaceSince = time.Now()

	dwell := s.handlebarPositionDwell
	s.log.Debug("handlebar moved off-place, starting dwell", "prev", prev, "dwell", dwell)
	s.handlebarDwellTimer = time.AfterFunc(dwell, func() { s.fireHandlebarDwell(gen) })
}

func (s *Subscriber) cancelHandlebarDwell() {
	s.handlebarDwellMu.Lock()
	defer s.handlebarDwellMu.Unlock()

	if s.handlebarDwellTimer == nil {
		return
	}
	s.handlebarDwellTimer.Stop()
	s.handlebarDwellTimer = nil

	// Timer.Stop cannot invalidate a callback already waiting on this mutex.
	s.handlebarDwellGen++
	s.log.Info("handlebar off-place returned within dwell, ignored",
		"off_place_ms", time.Since(s.handlebarOffPlaceSince).Milliseconds())
}

func (s *Subscriber) fireHandlebarDwell(gen uint64) {
	s.handlebarDwellMu.Lock()
	if gen != s.handlebarDwellGen {
		s.handlebarDwellMu.Unlock()
		return
	}
	s.handlebarDwellTimer = nil
	held := time.Since(s.handlebarOffPlaceSince)
	s.handlebarDwellMu.Unlock()

	// The setting is a live safety kill switch, including for an already-armed timer.
	if !s.handlebarTriggerEnabled.Load() {
		s.log.Debug("handlebar dwell expired but trigger disabled meanwhile")
		return
	}

	s.log.Info("handlebar held off-place past dwell, sending input trigger",
		"off_place_ms", held.Milliseconds())
	s.sm.SendEvent(fsm.InputTriggerEvent{Source: fsm.TriggerSourceHandlebarPosition})
}

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

func (s *Subscriber) setupPowerManagerWatcher() {
	s.powerManagerWatcher.OnField("state", func(stateStr string) error {
		imminent := isHibernatingImminentState(stateStr)
		s.log.Debug("power-manager state changed", "state", stateStr, "hibernation_imminent", imminent)
		s.sm.SendEvent(fsm.HibernationImminentEvent{Imminent: imminent})
		return nil
	})
}

// ums-by-dbc is the dashboard-initiated variant; both keep the gadget in mass
// storage until the session ends.
func isUMSMode(mode string) bool {
	return mode == "ums" || mode == "ums-by-dbc"
}

func (s *Subscriber) setupUSBWatcher() {
	s.usbWatcher.OnField("mode", func(mode string) error {
		active := isUMSMode(mode)
		s.log.Info("usb mode changed", "mode", mode, "ums_active", active)
		s.sm.SendEvent(fsm.UMSModeChangedEvent{Active: active})
		return nil
	})
}

// StartWithSync establishes all hash state before InitComplete, avoiding a
// separate read and preserving FSM startup ordering.
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

	if err := s.usbWatcher.StartWithSync(); err != nil {
		return fmt.Errorf("failed to start usb watcher: %w", err)
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

// Only press edges from physical tamper controls count; blinkers share this
// channel but are navigation signals, not alarm triggers.
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

func (s *Subscriber) Stop() {
	s.cancelHandlebarDwell()
	if err := s.vehicleWatcher.Stop(); err != nil {
		s.log.Warn("failed to stop vehicle watcher", "error", err)
	}
	if err := s.settingsWatcher.Stop(); err != nil {
		s.log.Warn("failed to stop settings watcher", "error", err)
	}
	if err := s.powerManagerWatcher.Stop(); err != nil {
		s.log.Warn("failed to stop power-manager watcher", "error", err)
	}
	if err := s.usbWatcher.Stop(); err != nil {
		s.log.Warn("failed to stop usb watcher", "error", err)
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
