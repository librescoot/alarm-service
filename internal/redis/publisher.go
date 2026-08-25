package redis

import (
	"fmt"
	"time"

	ipc "github.com/librescoot/redis-ipc"
)

// Publisher handles publishing alarm status to Redis
type Publisher struct {
	alarmPub *ipc.HashPublisher
	ipc      *ipc.Client
}

// NewPublisher creates a new Publisher
func NewPublisher(client *Client) *Publisher {
	return &Publisher{
		alarmPub: client.ipc.NewHashPublisher("alarm"),
		ipc:      client.ipc,
	}
}

// PublishStatus publishes alarm status using HashPublisher
func (p *Publisher) PublishStatus(status string) error {
	if err := p.alarmPub.Set("status", status); err != nil {
		return fmt.Errorf("failed to publish alarm status: %w", err)
	}
	return nil
}

// PublishTrigger records what set the alarm off. Both fields go out in one
// round trip with a single notification, so a consumer watching the hash is
// woken once and never sees a source paired with the previous timestamp.
// Timestamp format matches vehicle[state:timestamp].
func (p *Publisher) PublishTrigger(source string, at time.Time) error {
	fields := map[string]any{
		"trigger:source":    source,
		"trigger:timestamp": at.UTC().Format(time.RFC3339),
	}
	if err := p.alarmPub.SetManyPublishOne(fields, "trigger:source"); err != nil {
		return fmt.Errorf("failed to publish alarm trigger: %w", err)
	}
	return nil
}

// RequestHibernate sends a hibernate-manual command to pm-service
func (p *Publisher) RequestHibernate() error {
	if _, err := p.ipc.LPush("scooter:power", "hibernate-manual"); err != nil {
		return fmt.Errorf("failed to send hibernate command: %w", err)
	}
	return nil
}
