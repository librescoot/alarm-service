package redis

import (
	"fmt"

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

// PublishInterrupt publishes a BMX interrupt event to channel
func (p *Publisher) PublishInterrupt(payload string) error {
	if _, err := p.ipc.Publish("bmx:interrupt", payload); err != nil {
		return fmt.Errorf("failed to publish bmx interrupt: %w", err)
	}
	return nil
}
