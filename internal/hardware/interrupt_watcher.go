package hardware

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync/atomic"
	"time"

	"alarm-service/internal/hardware/bmx"
	"alarm-service/internal/redis"
)

const (
	// evdev event types and sizes for a 32-bit ARM target.
	evdevTypeKey   = 0x01
	evdevEventSize = 16
)

// InterruptWatcher reads the gpio-keys input event device for a specific
// keycode and publishes motion events the moment the BMX055 INT1 line rises,
// clearing the latched interrupt on the chip side. It runs alongside the
// I2C-based InterruptPoller: the watcher is the primary zero-latency path,
// the poller is a slow-tick watchdog that covers any missed edges.
type InterruptWatcher struct {
	devicePath string
	keycode    uint16
	accel      *bmx.Accelerometer
	publisher  *redis.Publisher
	log        *slog.Logger

	file    *os.File
	enabled atomic.Bool
}

// NewInterruptWatcher returns a watcher that opens devicePath and filters
// for key-press events matching keycode.
func NewInterruptWatcher(
	devicePath string,
	keycode uint16,
	accel *bmx.Accelerometer,
	publisher *redis.Publisher,
	log *slog.Logger,
) *InterruptWatcher {
	return &InterruptWatcher{
		devicePath: devicePath,
		keycode:    keycode,
		accel:      accel,
		publisher:  publisher,
		log:        log.With("evdev", devicePath, "keycode", keycode),
	}
}

// Open opens the input device. Returns an error if the device is missing so
// the caller can decide to fall back to polling-only mode.
func (w *InterruptWatcher) Open() error {
	f, err := os.OpenFile(w.devicePath, os.O_RDONLY, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", w.devicePath, err)
	}
	w.file = f
	w.log.Info("interrupt watcher opened")
	return nil
}

// Close releases the input device and unblocks any outstanding read by
// closing the fd.
func (w *InterruptWatcher) Close() {
	if w.file != nil {
		w.file.Close()
		w.file = nil
		w.log.Info("interrupt watcher closed")
	}
}

// Enable starts publishing motion events and clearing the latch when the
// configured keycode is pressed. Events that arrive while disabled are
// dropped — the FSM is typically not in an armed state then and the
// BMX055 should not be driving INT1 at all.
func (w *InterruptWatcher) Enable() {
	w.enabled.Store(true)
	w.log.Info("interrupt watcher enabled")
}

// Disable stops publishing motion events.
func (w *InterruptWatcher) Disable() {
	w.enabled.Store(false)
	w.log.Info("interrupt watcher disabled")
}

// Run reads input events until ctx is cancelled or the device closes.
// Blocking read on the input fd is unblocked by Close().
func (w *InterruptWatcher) Run(ctx context.Context) {
	w.log.Info("starting interrupt watcher")
	defer w.Close()

	buf := make([]byte, evdevEventSize)
	for {
		if ctx.Err() != nil {
			return
		}
		n, err := w.file.Read(buf)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, os.ErrClosed) || ctx.Err() != nil {
				return
			}
			w.log.Error("input read failed", "error", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		if n != evdevEventSize {
			w.log.Warn("short evdev read", "bytes", n)
			continue
		}

		typ := binary.LittleEndian.Uint16(buf[8:10])
		code := binary.LittleEndian.Uint16(buf[10:12])
		val := int32(binary.LittleEndian.Uint32(buf[12:16]))

		if typ != evdevTypeKey || code != w.keycode || val != 1 {
			continue
		}

		if !w.enabled.Load() {
			w.log.Debug("dropping interrupt edge (watcher disabled)")
			continue
		}

		w.handleEdge()
	}
}

// handleEdge publishes the motion event and clears the BMX055 latch.
// Publish first so the FSM sees the event even if the I2C clear fails
// (the poller's next tick will try again).
func (w *InterruptWatcher) handleEdge() {
	ts := time.Now().UnixMilli()
	w.log.Info("motion interrupt edge", "timestamp", ts)

	payload := fmt.Sprintf("%d", ts)
	if err := w.publisher.PublishInterrupt(payload); err != nil {
		w.log.Error("failed to publish interrupt", "error", err)
	}

	if err := w.accel.ClearLatchedInterrupt(); err != nil {
		w.log.Warn("failed to clear latched interrupt", "error", err)
	}
}
