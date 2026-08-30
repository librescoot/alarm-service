package redis

import (
	"fmt"
	"time"

	ipc "github.com/librescoot/redis-ipc"
)

type Publisher struct {
	alarmPub *ipc.HashPublisher
	ipc      *ipc.Client
}

func NewPublisher(client *Client) *Publisher {
	return &Publisher{
		alarmPub: client.ipc.NewHashPublisher("alarm"),
		ipc:      client.ipc,
	}
}

func (p *Publisher) PublishStatus(status string) error {
	if err := p.alarmPub.Set("status", status); err != nil {
		return fmt.Errorf("failed to publish alarm status: %w", err)
	}
	return nil
}

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

func (p *Publisher) RequestHibernate() error {
	if _, err := p.ipc.LPush("scooter:power", "hibernate-manual"); err != nil {
		return fmt.Errorf("failed to send hibernate command: %w", err)
	}
	return nil
}
