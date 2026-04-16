package hardware

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/warthog618/go-gpiocdev"
)

// InterruptWatcher subscribes to edge events on the BMX055 INT1 GPIO line
// and logs them alongside the I2C-based InterruptPoller. It is not the
// source of truth for motion events yet — see follow-up beans for the
// staged switch.
type InterruptWatcher struct {
	chipName string
	offset   int
	log      *slog.Logger

	chip    *gpiocdev.Chip
	line    *gpiocdev.Line
	enabled atomic.Bool

	mu        sync.Mutex
	lastEdge  time.Time
	edgeCount uint64
}

// NewInterruptWatcher creates a watcher for the given gpiochip and line offset.
// Typical values on this board: chipName="gpiochip2", offset=22 (LCD_DATA17 /
// GPIO3_IO22). The line must already be muxed as GPIO — verify with
// `cat /sys/kernel/debug/gpio` before enabling.
func NewInterruptWatcher(chipName string, offset int, log *slog.Logger) *InterruptWatcher {
	return &InterruptWatcher{
		chipName: chipName,
		offset:   offset,
		log:      log.With("gpio", fmt.Sprintf("%s:%d", chipName, offset)),
	}
}

// Open claims the GPIO line with rising-edge detection. INT1 is configured
// by the accelerometer driver as active-high push-pull, so a rising edge
// corresponds to the BMX055 asserting the interrupt.
func (w *InterruptWatcher) Open() error {
	chip, err := gpiocdev.NewChip(w.chipName, gpiocdev.WithConsumer("alarm-service"))
	if err != nil {
		return fmt.Errorf("open %s: %w", w.chipName, err)
	}

	line, err := chip.RequestLine(w.offset,
		gpiocdev.AsInput,
		gpiocdev.WithRisingEdge,
		gpiocdev.WithEventHandler(w.handleEvent),
		gpiocdev.WithConsumer("alarm-service"),
	)
	if err != nil {
		chip.Close()
		return fmt.Errorf("request line %d: %w", w.offset, err)
	}

	w.chip = chip
	w.line = line
	w.log.Info("interrupt watcher opened")
	return nil
}

// Close releases the GPIO line and chip.
func (w *InterruptWatcher) Close() {
	if w.line != nil {
		w.line.Close()
		w.line = nil
	}
	if w.chip != nil {
		w.chip.Close()
		w.chip = nil
	}
	w.log.Info("interrupt watcher closed")
}

// Enable starts logging edges. Edges that arrive while disabled are
// dropped silently — they typically correspond to states where the
// accelerometer has the interrupt disabled or remapped away.
func (w *InterruptWatcher) Enable() {
	w.enabled.Store(true)
	w.log.Info("interrupt watcher enabled")
}

// Disable stops logging edges.
func (w *InterruptWatcher) Disable() {
	w.enabled.Store(false)
	w.log.Info("interrupt watcher disabled")
}

// Run blocks until ctx is cancelled, then closes the watcher. Edge
// events are delivered asynchronously by the gpiocdev event handler.
func (w *InterruptWatcher) Run(ctx context.Context) {
	<-ctx.Done()
	w.Close()
}

// Stats returns the total edges seen and when the last one arrived.
func (w *InterruptWatcher) Stats() (count uint64, last time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.edgeCount, w.lastEdge
}

func (w *InterruptWatcher) handleEvent(evt gpiocdev.LineEvent) {
	if !w.enabled.Load() {
		return
	}
	now := time.Now()

	w.mu.Lock()
	w.edgeCount++
	count := w.edgeCount
	prev := w.lastEdge
	w.lastEdge = now
	w.mu.Unlock()

	attrs := []any{
		"edge", edgeTypeString(evt.Type),
		"seqno", evt.Seqno,
		"line_seqno", evt.LineSeqno,
		"count", count,
	}
	if !prev.IsZero() {
		attrs = append(attrs, "since_last", now.Sub(prev))
	}
	w.log.Info("motion interrupt edge", attrs...)
}

func edgeTypeString(t gpiocdev.LineEventType) string {
	switch t {
	case gpiocdev.LineEventRisingEdge:
		return "rising"
	case gpiocdev.LineEventFallingEdge:
		return "falling"
	default:
		return fmt.Sprintf("unknown(%d)", t)
	}
}
