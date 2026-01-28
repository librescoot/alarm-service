package pm

import (
	"fmt"
	"log/slog"
	"sync"
	"syscall"

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
	if err := i.Release(); err != nil {
		return err
	}
	return i.conn.Close()
}

// Acquire acquires a suspend inhibitor lock
func (i *Inhibitor) Acquire(reason string) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	if i.hasLock && i.lastReason == reason {
		i.log.Debug("already have inhibitor lock", "reason", reason)
		return nil
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

	var newFd dbus.UnixFD
	if err := call.Store(&newFd); err != nil {
		return fmt.Errorf("failed to store inhibitor fd: %w", err)
	}

	// Release old lock only after successfully acquiring new one
	if i.hasLock {
		if err := syscall.Close(int(i.fd)); err != nil {
			i.log.Warn("error closing old inhibitor fd", "error", err)
		}
	}

	i.fd = newFd
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

	if err := syscall.Close(int(i.fd)); err != nil {
		i.log.Warn("error closing inhibitor fd", "error", err)
	}

	i.hasLock = false
	i.lastReason = ""
	i.log.Info("released suspend inhibitor")

	return nil
}
