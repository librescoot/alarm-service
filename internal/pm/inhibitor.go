package pm

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	ipc "github.com/librescoot/redis-ipc"
)

// pm-service decides whether to suspend by consulting its own inhibitor
// registry: the power:inhibits Redis hash and the /tmp/suspend_inhibitor
// socket. To keep the MDB awake during an alarm we register a block inhibitor
// in power:inhibits, exactly as modem-service does for "modem is powered".
const (
	powerInhibitHash    = "power:inhibits"
	powerInhibitChannel = "power:inhibits"
	inhibitID           = "alarm-active"
	// Must differ from "librescoot-modem": pm-service's hasOnlyModemBlockingInhibitors
	// suspends anyway when the modem is the sole blocker. The alarm needs to read
	// as a genuine blocker.
	inhibitWho = "librescoot-alarm"
)

// inhibitData mirrors the JSON shape pm-service reads from the power:inhibits hash.
type inhibitData struct {
	ID       string `json:"id"`
	Who      string `json:"who"`
	What     string `json:"what"`
	Why      string `json:"why"`
	Type     string `json:"type"`
	Duration int64  `json:"duration"`
	Created  int64  `json:"created"`
}

func buildInhibitPayload(reason string) (string, error) {
	payload, err := json.Marshal(inhibitData{
		ID:      inhibitID,
		Who:     inhibitWho,
		What:    "power-state-change",
		Why:     reason,
		Type:    "block",
		Created: time.Now().Unix(),
	})
	if err != nil {
		return "", fmt.Errorf("marshal alarm inhibitor: %w", err)
	}
	return string(payload), nil
}

// Inhibitor holds a pm-service block inhibitor for the duration of the alarm's
// wake-lock states, blocking suspend/hibernate while the alarm is active.
type Inhibitor struct {
	client     *ipc.Client
	log        *slog.Logger
	mu         sync.Mutex
	hasLock    bool
	lastReason string
}

// NewInhibitor creates a suspend inhibitor backed by the power:inhibits hash.
// It clears any stale alarm-active entry left by a previous (crashed) instance
// so a held block can never wedge power management across a restart.
func NewInhibitor(client *ipc.Client, log *slog.Logger) (*Inhibitor, error) {
	i := &Inhibitor{
		client: client,
		log:    log,
	}
	if err := i.clear(); err != nil {
		// Non-fatal: the field may simply not exist. Log and continue.
		i.log.Warn("failed to clear stale alarm inhibitor on startup", "error", err)
	}
	return i, nil
}

// Close releases any held lock.
func (i *Inhibitor) Close() error {
	return i.Release()
}

// Acquire asserts the block inhibitor. Idempotent: re-asserting with the same
// reason is a no-op; a new reason rewrites the payload (pm-service reconciles
// from HGETALL, so an overwrite is safe).
func (i *Inhibitor) Acquire(reason string) error {
	i.mu.Lock()
	defer i.mu.Unlock()

	if i.hasLock && i.lastReason == reason {
		i.log.Debug("already have inhibitor lock", "reason", reason)
		return nil
	}

	payload, err := buildInhibitPayload(reason)
	if err != nil {
		return err
	}
	if err := i.client.HSet(powerInhibitHash, inhibitID, payload); err != nil {
		return fmt.Errorf("set alarm inhibitor: %w", err)
	}
	if _, err := i.client.Publish(powerInhibitChannel, "add:"+inhibitID); err != nil {
		return fmt.Errorf("publish alarm inhibitor add: %w", err)
	}

	i.hasLock = true
	i.lastReason = reason
	i.log.Info("acquired suspend inhibitor", "reason", reason)
	return nil
}

// Release clears the block inhibitor.
func (i *Inhibitor) Release() error {
	i.mu.Lock()
	defer i.mu.Unlock()

	if !i.hasLock {
		return nil
	}
	if err := i.clear(); err != nil {
		return err
	}
	i.hasLock = false
	i.lastReason = ""
	i.log.Info("released suspend inhibitor")
	return nil
}

// clear removes the alarm inhibitor from the hash and notifies pm-service. HDEL
// is required: pm-service reconciles from the hash, so publishing "remove:"
// without deleting the field would leave the inhibitor asserted.
func (i *Inhibitor) clear() error {
	if _, err := i.client.Do("HDEL", powerInhibitHash, inhibitID); err != nil {
		return fmt.Errorf("del alarm inhibitor: %w", err)
	}
	if _, err := i.client.Publish(powerInhibitChannel, "remove:"+inhibitID); err != nil {
		return fmt.Errorf("publish alarm inhibitor remove: %w", err)
	}
	return nil
}
