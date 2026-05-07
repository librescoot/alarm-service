package redis

import (
	"context"
	"fmt"
	"strconv"
	"time"

	ipc "github.com/librescoot/redis-ipc"
)


// motion-service RPC channel + method names. Kept here, not pulled from
// the motion-service repo, to avoid an import dependency between the two
// service repos. If motion-service ever changes its protocol, this is the
// one file that changes.
const (
	motionRPCChannel              = "motion:rpc"
	motionMethodPrepareHibernation = "prepare-hibernation"

	// Hash + field where motion-service stamps a wake-from-hibernation
	// indicator on its startup if it found a pre-existing latched interrupt.
	// Persistent so we don't lose the signal to a startup-ordering race
	// with the pub/sub motion:interrupt channel.
	motionHash         = "motion"
	motionWakeCauseFld = "wake-cause"
)

// PrepareHibernationReq is the wire payload for the synchronous chip-config
// confirmation alarm-service performs before letting pm-service suspend.
type PrepareHibernationReq struct {
	Profile string `json:"profile"`
}

// PrepareHibernationResp is what motion-service answers with.
type PrepareHibernationResp struct {
	Programmed bool   `json:"programmed"`
	Profile    string `json:"profile"`
}

// MotionClient is a thin wrapper over the redis-ipc Call primitive for the
// motion-service RPC surface alarm-service depends on. Only the synchronous
// hibernation handshake is exposed today; everything else (profile changes
// for arm/disarm/L1/L2) flows reactively through the alarm hash that
// motion-service watches.
//
// Two ipc clients: `bus` for the existing string-codec uses (HGet/HDel
// of motion.wake-cause); `rpc` is a dedicated JSON-codec client used for
// CallMethod, since the typed Req/Resp encoding goes through the
// client's codec. Pool size 4 on the rpc client — overkill for one
// caller-side conn but cheap and gives headroom for future extra RPCs.
type MotionClient struct {
	bus *ipc.Client
	rpc *ipc.Client
}

// NewMotionClient returns a client. `bus` should be the alarm-service's
// existing redis-ipc client (StringCodec); the function spins up a
// parallel JSON-codec client for the Call work.
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

// Close shuts down the dedicated rpc ipc client.
func (m *MotionClient) Close() error {
	if m.rpc != nil {
		return m.rpc.Close()
	}
	return nil
}

// splitHostPort splits "host:port" into ("host", port). Default port 6379
// if not specified or unparseable.
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

// PrepareHibernation synchronously asks motion-service to confirm the chip
// is in armed-hibernation profile. Used as the gating handshake before
// alarm-service releases the pm-service suspend inhibitor.
//
// 1.5 s timeout matches pm-service's SuspendImminentDelay (5 s) with margin.
// motion-service's apply path is well under 200 ms on the bench; the timeout
// is for I2C wedge / motion-service crash-restart cases.
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

// ConsumeWakeCause reads + clears the motion.wake-cause field. Returns
// true if motion-service stamped a wake-from-hibernation indicator and
// the timestamp is recent (within 30 s of now). The field is deleted
// after consumption so a subsequent alarm-service restart doesn't pick
// it up again.
func (m *MotionClient) ConsumeWakeCause(ctx context.Context) (bool, error) {
	val, err := m.bus.HGet(motionHash, motionWakeCauseFld)
	if err != nil {
		if err == ipc.ErrNil {
			return false, nil
		}
		return false, fmt.Errorf("HGet %s.%s: %w", motionHash, motionWakeCauseFld, err)
	}
	// Always best-effort delete so a stale value can't pollute the next start.
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
