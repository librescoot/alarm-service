package pm

import (
	"fmt"
	"log/slog"
	"sync"

	"github.com/godbus/dbus/v5"
)

// Inhibitor manages systemd suspend inhibitor locks
type Inhibitor struct {
	conn       *dbus.Conn
	log        *slog.Logger
	mu         sync.Mutex
	fd         dbus.UnixFD
	hasLock    bool
	lastReason string
}

// NewInhibitor creates a new suspend inhibitor
func NewInhibitor(log *slog.Logger) (*Inhibitor, error) {
	conn, err := dbus.SystemBus()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to system bus: %w", err)
	}

	return &Inhibitor{
		conn:    conn,
		log:     log,
		hasLock: false,
	}, nil
}

// Close closes the inhibitor and releases any held locks
func (i *Inhibitor) Close() error {
	i.Release()
	return i.conn.Close()
}

// Acquire acquires a suspend inhibitor lock
func (i *Inhibitor) Acquire(reason string) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	if i.hasLock {
		if i.lastReason == reason {
			i.log.Debug("already have inhibitor lock", "reason", reason)
			return nil
		}
		i.releaseUnsafe()
	}

	obj := i.conn.Object("org.freedesktop.login1", "/org/freedesktop/login1")

	call := obj.Call("org.freedesktop.login1.Manager.Inhibit", 0,
		"sleep",
		"alarm-service",
		reason,
		"block")

	if call.Err != nil {
		return fmt.Errorf("failed to acquire inhibitor lock: %w", call.Err)
	}

	if err := call.Store(&i.fd); err != nil {
		return fmt.Errorf("failed to store inhibitor fd: %w", err)
	}

	i.hasLock = true
	i.lastReason = reason
	i.log.Info("acquired suspend inhibitor", "reason", reason)

	return nil
}

// Release releases the suspend inhibitor lock
func (i *Inhibitor) Release() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.releaseUnsafe()
}

// releaseUnsafe releases the lock without locking (internal use)
func (i *Inhibitor) releaseUnsafe() error {
	if !i.hasLock {
		return nil
	}

	if err := i.conn.Close(); err != nil {
		i.log.Warn("error closing inhibitor fd", "error", err)
	}

	conn, err := dbus.SystemBus()
	if err != nil {
		return fmt.Errorf("failed to reconnect to system bus: %w", err)
	}
	i.conn = conn

	i.hasLock = false
	i.lastReason = ""
	i.log.Info("released suspend inhibitor")

	return nil
}