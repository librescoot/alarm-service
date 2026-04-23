# alarm-service

Alarm state machine service for LibreScoot motion-based alarm system.

## Features

- 8-state finite state machine for alarm logic
- **Integrated BMX055 hardware control** (no separate bmx-service required)
- Direct I2C communication with accelerometer and gyroscope
- Multi-level triggering (Level 1: notification, Level 2: alarm with horn + hazards)
- Automatic BMX configuration based on alarm state
- Suspend inhibitor management (wake locks)
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
                                         |        trigger_level_2 (50s, max 4 cycles)
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
- `HGET settings alarm.duration` - Alarm duration in seconds
- `HGET settings alarm.hairtrigger` - Short honk on first motion (true/false)
- `HGET settings alarm.hairtrigger-duration` - Hair-trigger honk duration in seconds
- `HGET settings alarm.l1-cooldown` - Level 1 cooldown duration in seconds
- `HGET settings alarm.seatbox-trigger` - Unauthorized seatbox opening triggers alarm (true/false)
- `HGET settings alarm.sensitivity` - Motion sensitivity in armed state: `low`, `medium`, `high`
- `HGET settings alarm.trigger.motion` - BMX055 motion triggers alarm (true/false, default true)
- `HGET settings alarm.trigger.buttons` - Brake/horn/seatbox button presses trigger alarm (true/false, default true)
- `HGET settings alarm.trigger.handlebar` - Handlebar lock sensor / position triggers alarm (true/false, default true)

### Trigger Sources

Each source can be disabled independently via its `alarm.trigger.*` setting. Defaults are all-on. For the Berlin-vibration "inputs-only" mode, set `alarm.trigger.motion=false` and leave the others on.

| Source | Signal | Needs vehicle-service in Standby |
|---|---|---|
| Motion | `bmx:interrupt` channel (BMX055 accelerometer) | — |
| Buttons | `buttons` channel (brake:{left,right}:on, seatbox:on, horn:on) | Already live in Standby |
| Handlebar lock | `vehicle.handlebar:lock-sensor` = unlocked | Already live in Standby |
| Handlebar position | `vehicle.handlebar:position` = off-place | Already live in Standby |
| Seatbox lock | `vehicle.seatbox:lock` = open + no authorized-open event | Already live in Standby |

#### Throttle is not a supported trigger source

The thumb throttle is a Hall sensor wired to the Bosch/Votol ECU, not to the MDB. Its state is only visible as a CAN-bus payload from the ECU. In Standby the ECU's 12V rail is cut — CAN goes silent, no throttle data flows. Exposing throttle to the alarm service would require keeping the ECU powered while the scooter is locked, which defeats Standby's power budget. Kickstand, brakes, handlebar lock, and handlebar position cover the "deliberate physical input" use case from [librescoot issue #26](https://github.com/librescoot/librescoot/issues/26) without that cost.

### Subscribed Channels

- `vehicle` - Vehicle state + seatbox lock + handlebar lock/position updates
- `settings` - Settings changes (alarm.*)
- `bmx:interrupt` - Motion detection from integrated BMX055 hardware
- `buttons` - Physical button edges from vehicle-service (brake/seatbox/horn/blinker)

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

| State | Wake Lock | Sensor config | INT Pin |
|-------|-----------|---------------|---------|
| armed | No | any-motion, bandwidth 31.25 Hz, threshold per `alarm.sensitivity` (low=0x08, medium=0x04, high=0x02) | BOTH |
| delay_armed | Yes | slow-motion idle (threshold 0x14) | INT2 |
| trigger_level_1 | Yes | slow-motion, bandwidth 15.63 Hz, threshold 0x08 | BOTH |
| trigger_level_2 | Yes | slow-motion idle | — |
| waiting_movement | Yes | slow-motion, bandwidth 7.81 Hz, threshold 0x06 (at t=47s) | NONE |

`alarm.sensitivity` tunes the armed-state threshold only; escalation stages (L1 confirm, L2 re-trigger) stay fixed.

## License

This project is licensed under the [Creative Commons Attribution-NonCommercial 4.0 International License](LICENSE).

Non-commercial use only. Commercial use is prohibited.
