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

### Subscribed Channels

- `vehicle` - Vehicle state changes (payload: "state")
- `settings` - Settings changes (payload: "alarm.enabled" or "alarm.honk")
- `bmx:interrupt` - Motion detection from integrated BMX055 hardware

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

| State | Wake Lock | Sensitivity | INT Pin |
|-------|-----------|-------------|---------|
| armed | No | MEDIUM | NONE |
| delay_armed | Yes | LOW | INT2 |
| trigger_level_1 | Yes | MEDIUM | NONE |
| trigger_level_2 | Yes | HIGH | NONE |

## License

This project is licensed under the [Creative Commons Attribution-NonCommercial 4.0 International License](LICENSE).

Non-commercial use only. Commercial use is prohibited.
