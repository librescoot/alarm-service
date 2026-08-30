package pm

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	ipc "github.com/librescoot/redis-ipc"
)

// pm-service reconciles this hash and channel; "block" must prevent hibernate
// as well as suspend, because either power state silences the alarm.
const (
	powerInhibitHash    = "power:inhibits"
	powerInhibitChannel = "power:inhibits"
	inhibitID           = "alarm-active"

	// Must not identify as the modem: pm-service may suspend with only that blocker.
	inhibitWho = "librescoot-alarm"
)

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

type Inhibitor struct {
	client     *ipc.Client
	log        *slog.Logger
	mu         sync.Mutex
	hasLock    bool
	lastReason string
}

func NewInhibitor(client *ipc.Client, log *slog.Logger) (*Inhibitor, error) {
	i := &Inhibitor{
		client: client,
		log:    log,
	}
	// A prior crash can leave a stale suspend inhibitor.
	if err := i.clear(); err != nil {

		i.log.Warn("failed to clear stale alarm inhibitor on startup", "error", err)
	}
	return i, nil
}

func (i *Inhibitor) Close() error {
	return i.Release()
}

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

// clear deletes before notifying: a notification alone is reasserted by
// pm-service's next hash reconciliation.
func (i *Inhibitor) clear() error {
	if _, err := i.client.Do("HDEL", powerInhibitHash, inhibitID); err != nil {
		return fmt.Errorf("del alarm inhibitor: %w", err)
	}
	if _, err := i.client.Publish(powerInhibitChannel, "remove:"+inhibitID); err != nil {
		return fmt.Errorf("publish alarm inhibitor remove: %w", err)
	}
	return nil
}
