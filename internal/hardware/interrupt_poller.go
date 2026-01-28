package hardware

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"alarm-service/internal/hardware/bmx"
	"alarm-service/internal/redis"
)

// InterruptPoller monitors for motion interrupts and publishes to Redis
type InterruptPoller struct {
	accel     *bmx.Accelerometer
	gyro      *bmx.Gyroscope
	publisher *redis.Publisher
	log       *slog.Logger
	enabled   atomic.Bool
}

// NewInterruptPoller creates a new InterruptPoller
func NewInterruptPoller(
	accel *bmx.Accelerometer,
	gyro *bmx.Gyroscope,
	publisher *redis.Publisher,
	log *slog.Logger,
) *InterruptPoller {
	return &InterruptPoller{
		accel:     accel,
		gyro:      gyro,
		publisher: publisher,
		log:       log,
	}
}

// Enable enables interrupt monitoring
func (p *InterruptPoller) Enable() {
	p.enabled.Store(true)
	p.log.Info("interrupt monitoring enabled")
}

// Disable disables interrupt monitoring
func (p *InterruptPoller) Disable() {
	p.enabled.Store(false)
	p.log.Info("interrupt monitoring disabled")
}

// Run starts the interrupt polling loop
func (p *InterruptPoller) Run(ctx context.Context) {
	p.log.Info("starting interrupt poller")

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			p.log.Info("interrupt poller stopped")
			return

		case <-ticker.C:
			if p.enabled.Load() {
				if err := p.checkInterrupt(ctx); err != nil {
					p.log.Error("failed to check interrupt", "error", err)
				}
			}
		}
	}
}

// checkInterrupt checks if an interrupt has occurred and publishes to Redis
func (p *InterruptPoller) checkInterrupt(ctx context.Context) error {
	triggered, err := p.accel.GetInterruptStatus()
	if err != nil {
		return err
	}

	if !triggered {
		return nil
	}

	timestamp := time.Now().UnixMilli()
	p.log.Info("motion interrupt detected", "timestamp", timestamp)

	payload := fmt.Sprintf("%d", timestamp)
	if err := p.publisher.PublishInterrupt(payload); err != nil {
		return err
	}

	if err := p.accel.ClearLatchedInterrupt(); err != nil {
		return fmt.Errorf("failed to clear latched interrupt: %w", err)
	}

	return nil
}
