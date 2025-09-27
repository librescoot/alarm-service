package redis

import (
	"context"
	"fmt"
)

// Publisher handles publishing alarm status to Redis
type Publisher struct {
	client *Client
}

// NewPublisher creates a new Publisher
func NewPublisher(client *Client) *Publisher {
	return &Publisher{
		client: client,
	}
}

// PublishStatus publishes alarm status
func (p *Publisher) PublishStatus(ctx context.Context, status string) error {
	if err := p.client.HSet(ctx, "alarm", "status", status); err != nil {
		return fmt.Errorf("failed to set alarm status: %w", err)
	}

	if err := p.client.Publish(ctx, "alarm", status); err != nil {
		return fmt.Errorf("failed to publish alarm status: %w", err)
	}

	return nil
}

// PublishInterrupt publishes a BMX interrupt event
func (p *Publisher) PublishInterrupt(ctx context.Context, payload string) error {
	if err := p.client.Publish(ctx, "bmx:interrupt", payload); err != nil {
		return fmt.Errorf("failed to publish bmx interrupt: %w", err)
	}
	return nil
}