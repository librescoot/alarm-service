package redis

import (
	"context"
	"fmt"
	"strconv"
	"time"

	ipc "github.com/librescoot/redis-ipc"
)

// This mirrors motion-service's public RPC without an inter-repository import.
const (
	motionRPCChannel               = "motion:rpc"
	motionMethodPrepareHibernation = "prepare-hibernation"

	motionHash         = "motion"
	motionWakeCauseFld = "wake-cause"
)

type PrepareHibernationReq struct {
	Profile string `json:"profile"`
}

type PrepareHibernationResp struct {
	Programmed bool   `json:"programmed"`
	Profile    string `json:"profile"`
}

type MotionClient struct {
	bus *ipc.Client
	rpc *ipc.Client
}

func NewMotionClient(bus *ipc.Client) (*MotionClient, error) {
	addr, port := splitHostPort(bus.Raw().Options().Addr)
	rpc, err := ipc.New(
		ipc.WithAddress(addr),
		ipc.WithPort(port),
		ipc.WithPoolSize(4),
		ipc.WithCodec(ipc.JSONCodec{}),
		ipc.WithLogger(bus.Logger()),
	)
	if err != nil {
		return nil, fmt.Errorf("create motion rpc client: %w", err)
	}
	return &MotionClient{bus: bus, rpc: rpc}, nil
}

func (m *MotionClient) Close() error {
	if m.rpc != nil {
		return m.rpc.Close()
	}
	return nil
}

func splitHostPort(addr string) (string, int) {
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			port, err := strconv.Atoi(addr[i+1:])
			if err == nil {
				return addr[:i], port
			}
			return addr[:i], 6379
		}
	}
	return addr, 6379
}

// PrepareHibernation is the suspend-gating RPC; its timeout leaves margin
// inside pm-service's hibernation-imminent delay.
func (m *MotionClient) PrepareHibernation(ctx context.Context) error {
	resp, err := ipc.CallMethod[PrepareHibernationReq, PrepareHibernationResp](
		m.rpc,
		motionRPCChannel,
		motionMethodPrepareHibernation,
		PrepareHibernationReq{Profile: "armed-hibernation"},
		1500*time.Millisecond,
	)
	if err != nil {
		return fmt.Errorf("prepare-hibernation call: %w", err)
	}
	if !resp.Programmed {
		return fmt.Errorf("motion-service rejected prepare-hibernation: profile=%q", resp.Profile)
	}
	return nil
}

// Consume the durable, one-shot wake cause; the hash closes the pub/sub
// startup race and stale values are ignored.
func (m *MotionClient) ConsumeWakeCause(ctx context.Context) (bool, error) {
	val, err := m.bus.HGet(motionHash, motionWakeCauseFld)
	if err != nil {
		if err == ipc.ErrNil {
			return false, nil
		}
		return false, fmt.Errorf("HGet %s.%s: %w", motionHash, motionWakeCauseFld, err)
	}

	// Delete even malformed or stale values to prevent a replay.
	defer m.bus.Raw().HDel(m.bus.Context(), motionHash, motionWakeCauseFld)

	if val == "" {
		return false, nil
	}
	tsMs, err := strconv.ParseInt(val, 10, 64)
	if err != nil {
		return false, fmt.Errorf("malformed wake-cause %q: %w", val, err)
	}
	age := time.Since(time.UnixMilli(tsMs))
	if age > 30*time.Second {
		return false, nil
	}
	return true, nil
}
