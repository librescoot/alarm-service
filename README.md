# alarm-service

Alarm state machine service for LibreScoot motion-based alarm system.

## Features

- 8-state finite state machine for alarm logic
- Integration with bmx-service for motion detection
- Multi-level triggering (Level 1: notification, Level 2: alarm with horn + hazards)
- Automatic BMX configuration based on alarm state
- Suspend inhibitor management (wake locks)
- Horn pattern: 400ms on/off alternating
- Hazard lights: continuous during alarm

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
alarm-service --redis=localhost:6379
```

## Redis Interface

### Subscriptions

- `vehicle:state` - Vehicle state changes (standby, moving, etc.)
- `settings:alarm-mode` - Alarm enable/disable
- `bmx:interrupt` - Motion detection from bmx-service
- `scooter:alarm` - Manual alarm commands

### Publications

- `alarm` hash + channel - Current alarm status

### Commands Sent

- `scooter:bmx` - BMX configuration (sensitivity, pin, interrupt)
- `scooter:horn` - Horn control (on/off pattern)
- `scooter:blinker` - Hazard light control (both/off)

## Manual Alarm Control

```bash
# Start alarm for 30 seconds
redis-cli LPUSH scooter:alarm start:30

# Stop alarm immediately
redis-cli LPUSH scooter:alarm stop
```

## Testing

```bash
# Enable alarm
redis-cli -h 10.7.0.4 HSET settings alarm-mode enabled

# Set vehicle to standby
redis-cli -h 10.7.0.4 PUBLISH vehicle:state standby

# Monitor alarm status
redis-cli -h 10.7.0.4 SUBSCRIBE alarm

# Test manual alarm
redis-cli -h 10.7.0.4 LPUSH scooter:alarm start:10
```

## State-Specific Behavior

| State | Wake Lock | Sensitivity | INT Pin |
|-------|-----------|-------------|---------|
| armed | No | MEDIUM | NONE |
| delay_armed | Yes | LOW | INT2 |
| trigger_level_1 | Yes | MEDIUM | NONE |
| trigger_level_2 | Yes | HIGH | NONE |
