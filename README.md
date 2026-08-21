# alarm-service

Alarm state machine service for Librescoot motion-based alarm system.

Part of the [Librescoot](https://librescoot.org/) open-source platform.

## Features

- 8-state finite state machine for alarm logic
- **Integrated BMX055 hardware control** (no separate bmx-service required)
- Direct I2C communication with accelerometer and gyroscope
- Multi-level triggering (Level 1: notification, Level 2: alarm with horn + hazards)
- Automatic BMX configuration based on alarm state
- Suspend inhibitor management (wake locks): holds a `block` inhibitor in pm-service's `power:inhibits` hash while the alarm is armed-delaying or triggered, so the MDB cannot suspend or hibernate mid-alarm
- Horn pattern: 400ms on/off alternating
- Hazard lights: continuous during alarm

## Architecture

The service directly controls the BMX055 motion sensor via I2C:
- **Accelerometer (0x18)**: Slow/no-motion interrupt detection
- **Gyroscope (0x68)**: Rotation detection for interrupt validation
- **Interrupt Poller**: 100ms polling loop monitoring accelerometer interrupt status

BMX interrupts are published to Redis `bmx:interrupt` channel for state machine processing.

## State Machine

```
init → waiting_enabled → disarmed → delay_armed (5s) → armed
                                         ↑                ↓ motion
                                         |        trigger_level_1_wait (15s cooldown)
                                         |                ↓
                                         |        trigger_level_1 (5s check)
                                         |                ↓ major movement
                                         |        trigger_level_2 (50s, max 6 cycles ≈ 10min)
                                         |________________|
```

## Build

```bash
make build          # ARM binary for target
make build-amd64    # AMD64 binary
```

## Usage

```bash
alarm-service [flags]

Flags:
  --i2c-bus=/dev/i2c-3      I2C bus device path for BMX055
  --redis=localhost:6379    Redis address
  --log-level=info          Log level (debug, info, warn, error)
  --alarm-duration=10       Alarm duration in seconds
  --horn-enabled=false      Enable horn during alarm (overrides Redis setting)
  --version                 Print version and exit
```

### Configuration Override

- If `--horn-enabled` flag is explicitly set, it writes to Redis (`settings alarm.honk`) and overrides any existing value
- If flag is not set, the service reads from Redis
- This allows both persistent configuration via Redis and temporary overrides via CLI

## Redis Interface

### Settings Keys

- `HGET settings alarm.enabled` - Alarm enabled (true/false)
- `HGET settings alarm.honk` - Horn enabled during alarm (true/false)
- `HGET settings alarm.seatbox-trigger` - Unauthorized seatbox opening triggers the alarm (true/false)
- `HGET settings alarm.trigger.motion` - Motion is a trigger source (true/false, default true)
- `HGET settings alarm.trigger.buttons` - Brake/horn/seatbox button presses are a trigger source (true/false, default true)
- `HGET settings alarm.trigger.handlebar` - Handlebar lock sensor and position are a trigger source (true/false, default true)

### Trigger Sources

Every tamper source can be switched off on its own. All default to on, so an
untouched scooter behaves exactly as before.

| Setting | What it gates |
|---|---|
| `alarm.trigger.motion` | motion events from motion-service |
| `alarm.trigger.buttons` | brake left/right, horn and seatbox button presses on the `buttons` channel |
| `alarm.trigger.handlebar` | `vehicle.handlebar:lock-sensor` going unlocked, `vehicle.handlebar:position` going off-place |
| `alarm.seatbox-trigger` | unauthorized `vehicle.seatbox:lock=open` |

The handlebar sensors only count as tampering on a genuine safe-to-unsafe
transition seen after startup. A scooter parked with the handlebar lock never
engaged reports "unlocked" as its resting value, and that must not fire the
alarm every time the service restarts.

Both handlebar sources also stay muted for 90 seconds after the alarm arms.
Locking the vehicle is itself a handlebar event: vehicle-service pulses the
lock solenoid with retries and keeps a 60 second positioning window open for a
rider who still has to swing the bars into place, and either can bounce the
lock sensor or move the position sensor while the alarm is already armed. The
window restarts on every arm, and edges inside it are dropped rather than
replayed afterwards. Motion, buttons and the seatbox are not muted.

Buttons and handlebar events are filtered in the subscriber, so a source that
is switched off costs the state machine nothing. Motion is filtered in the
state machine instead, because motion events also carry the
wake-from-hibernation stamp that the re-hibernate cooldown depends on.

Switching a source off suppresses the alarm, not the wake. The accelerometer
still asserts its interrupt and the nRF52 still wakes the MDB;
`alarm.trigger.motion=false` only means the resulting event is dropped instead
of escalated.

Throttle is deliberately absent. It is only visible as a CAN payload from the
ECU, and the ECU is powered down in Standby.

### Subscribed Channels

- `vehicle` - Vehicle state changes (payload: "state")
- `settings` - Settings changes (payload: "alarm.enabled" or "alarm.honk")
- `bmx:interrupt` - Motion detection from integrated BMX055 hardware
- `buttons` - Button edges from vehicle-service (`brake:{left,right}:{on,off}`, `horn:{on,off}`, `seatbox:{on,off}`)

### Published Status

- `HGET alarm status` - Current alarm status (disabled, disarmed, armed, level-1-triggered, level-2-triggered)

### Commands Sent

- `scooter:bmx` - BMX configuration (sensitivity, pin, interrupt)
- `scooter:horn` - Horn control (on/off pattern)
- `scooter:blinker` - Hazard light control (both/off)

## Alarm Control

```bash
# Enable alarm system
redis-cli LPUSH scooter:alarm enable

# Disable alarm system
redis-cli LPUSH scooter:alarm disable

# Start alarm for 30 seconds (manual trigger)
redis-cli LPUSH scooter:alarm start:30

# Stop alarm immediately
redis-cli LPUSH scooter:alarm stop
```

## Testing

```bash
# Enable alarm
redis-cli HSET settings alarm.enabled true
redis-cli publish settings alarm.enabled

# Enable horn
redis-cli HSET settings alarm.honk true
redis-cli publish settings alarm.honk

# Set vehicle to standby (triggers arming)
redis-cli HSET vehicle state stand-by
redis-cli publish vehicle state

# Monitor alarm status
redis-cli SUBSCRIBE alarm

# Test manual alarm trigger
redis-cli LPUSH scooter:alarm start:10

# Or use command to enable/disable
redis-cli LPUSH scooter:alarm enable
```

## State-Specific Behavior

"Wake Lock" is a `block` inhibitor in pm-service's `power:inhibits` hash (id
`alarm-active`, who `librescoot-alarm`); pm-service holds off suspend and
hibernate while it is held. `armed` deliberately drops the lock so an
idle-armed scooter can still hibernate (motion-service rearms the chip to wake
on motion); a real motion trigger re-acquires it before the alarm sounds.

| State | Wake Lock | Sensitivity | INT Pin |
|-------|-----------|-------------|---------|
| armed | No | MEDIUM | NONE |
| delay_armed | Yes | LOW | INT2 |
| trigger_level_1 | Yes | MEDIUM | NONE |
| trigger_level_2 | Yes | HIGH | NONE |

## License

This project is dual-licensed. The source code is available under the
[Creative Commons Attribution-NonCommercial-ShareAlike 4.0 International License][cc-by-nc-sa].
The maintainers reserve the right to grant separate licenses for commercial distribution; please contact the maintainers to discuss commercial licensing.

[![CC BY-NC-SA 4.0][cc-by-nc-sa-image]][cc-by-nc-sa]

[cc-by-nc-sa]: http://creativecommons.org/licenses/by-nc-sa/4.0/
[cc-by-nc-sa-image]: https://licensebuttons.net/l/by-nc-sa/4.0/88x31.png
