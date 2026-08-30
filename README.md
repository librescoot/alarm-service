# Librescoot Alarm Service

Part of the [Librescoot](https://librescoot.org/) open-source platform.

## Overview

`alarm-service` implements the vehicle alarm state machine. It consumes vehicle, settings, button, and motion events from Redis/Valkey; publishes alarm status; and sends horn, blinker, and power commands through Redis queues. It does not program the BMX055 directly: [`motion-service`](../motion-service/README.md) owns the sensor and supplies motion events and a hibernation-preparation RPC.

## Capabilities

- Arms after the vehicle enters `stand-by`, with a five-second arming delay.
- Escalates motion, configured button, handlebar, or unauthorized seatbox events through level 1 and level 2 alarm states.
- Drives hazard lights and, when enabled, a 400 ms on/off horn pattern through vehicle command queues.
- Tracks the last state-changing trigger source and time in the `alarm` hash.
- Coordinates hibernation with motion-service and holds a power-manager inhibitor while arming, responding to triggers, or handling authorized seatbox access.
- Supports persistent settings and explicit command-line overrides.

## Operation and interfaces

### State machine

The primary status values published as `alarm.status` are `disabled`, `disarmed`, `delay-armed`, `armed`, `level-1-triggered`, `level-2-triggered`, and `seatbox-access`.

When armed, a motion or enabled input trigger enters a configurable level-1 cooldown (15 seconds by default) and then a five-second level-1 check. A subsequent tamper event escalates to level 2. An unauthorized seatbox opening escalates directly to level 2. Level 2 runs for 50-second checks and can repeat on further tamper events, up to six cycles. Disarming is driven by the vehicle leaving standby, disabling the alarm, or a runtime disarm command.

Handlebar triggers are disabled by default. When enabled, the service ignores the initial field value after startup, suppresses handlebar inputs for 90 seconds after arming, and requires `handlebar:position` to remain `off-place` for one second. These guards do not apply to motion, buttons, or seatbox triggers.

### Redis/Valkey contract

The service connects to `--redis` (default `localhost:6379`). It uses Redis/Valkey hash-watch notifications for `vehicle`, `settings`, and `power-manager`; publishers must use the matching hash notification mechanism.

| Interface | Direction | Contract |
|---|---|---|
| `vehicle` hash | reads | `state`, `seatbox:lock`, `handlebar:lock-sensor`, and `handlebar:position`; `seatbox:opened` represents an authorized opening. |
| `settings` hash | reads; CLI overrides write | `alarm.enabled`, `alarm.honk`, `alarm.duration`, `alarm.seatbox-trigger`, `alarm.hairtrigger`, `alarm.hairtrigger-duration`, `alarm.l1-cooldown`, `alarm.trigger.motion`, `alarm.trigger.buttons`, and `alarm.trigger.handlebar`. |
| `power-manager` hash | reads | `state` controls hibernation preparation while the alarm is armed. |
| `motion:interrupt` | subscribes | JSON object with `type`, millisecond `timestamp`, and optional `engine`; normally published by motion-service. |
| `motion` hash | reads and consumes | `wake-cause` is a one-shot millisecond timestamp. A valid value less than 30 seconds old is deleted after consumption. |
| `buttons` | subscribes | `brake:left:on`, `brake:right:on`, `horn:on`, or `seatbox:on` are input triggers when button triggering is enabled. |
| `alarm` hash | publishes | `status`, `trigger:source`, `trigger:timestamp` (UTC RFC3339), and `alarm-active`. |
| `scooter:horn`, `scooter:blinker` | pushes | Horn `on`/`off`; blinkers `both`/`off`. |
| `scooter:power` | pushes | `hibernate-manual` after the wake cooldown. |
| `power:inhibits` hash and channel | writes/publishes | A `block` inhibitor with ID `alarm-active`; add/remove notifications use `power:inhibits`. |

`motion-service` derives its profile from `alarm.status` and `power-manager.state`. Before an armed scooter hibernates, alarm-service calls method `prepare-hibernation` on `motion:rpc`; motion-service must confirm that it has programmed `armed-hibernation`. If that RPC fails, the alarm service retains a power inhibitor to prevent hibernation with an unconfirmed sensor configuration.

### Alarm commands

Commands are strings pushed onto Redis list `scooter:alarm`:

```sh
redis-cli LPUSH scooter:alarm enable
redis-cli LPUSH scooter:alarm disable
redis-cli LPUSH scooter:alarm arm
redis-cli LPUSH scooter:alarm disarm
redis-cli LPUSH scooter:alarm stop
redis-cli LPUSH scooter:alarm start:30
```

`enable` and `disable` write `settings.alarm.enabled`. `arm` and `disarm` are runtime state-machine commands and do not change that setting. `start:<seconds>` directly starts the horn/blinker controller.

## Configuration

All flags are optional:

```text
--redis ADDRESS
--log-level debug|info|warn|error
--alarm-enabled BOOL
--alarm-duration SECONDS
--horn-enabled BOOL
--seatbox-trigger BOOL
--hair-trigger BOOL
--hair-trigger-duration SECONDS
--l1-cooldown SECONDS
--version
```

Only explicitly supplied setting flags write their associated `settings.alarm.*` field at startup; unset flags do not replace the value already in Redis. `--log-level` and `--redis` are runtime-only. The defaults are alarm enabled, 30-second level-2 alarm duration, horn disabled, seatbox trigger enabled, hair trigger disabled, three-second hair-trigger duration, and 15-second level-1 cooldown.

## Build and test

Requires Go and the dependencies declared in `go.mod`.

```sh
make build        # static Linux ARMv7 binary: bin/alarm-service
make build-host   # host binary: bin/alarm-service
make test
make lint         # requires golangci-lint
```

`make run`, `make fmt`, `make deps`, and `make clean` are also available.

## Deployment and runtime dependencies

The Yocto package installs `/usr/bin/alarm-service` and systemd unit `librescoot-alarm.service`. The shipped unit starts after and wants `valkey.service` and `librescoot-motion.service`, passes `--redis=localhost:6379`, and runs as root.

It requires a functioning Redis/Valkey instance, vehicle services that consume the horn and blinker queues, a compatible power-manager inhibitor consumer, and motion-service for motion detection and hibernation coordination. It handles `SIGINT` and `SIGTERM` for orderly shutdown.

## Operational and security notes

- Alarm configuration and command queues can change vehicle signalling and power behaviour. Limit Redis/Valkey access to trusted local services and administrators.
- `alarm.trigger.motion=false` suppresses alarm escalation only; it does not prevent a motion interrupt from waking the platform.
- `alarm.trigger.buttons` and `alarm.trigger.handlebar` govern only their respective sources. Review the published `alarm.trigger:source` and `alarm.trigger:timestamp` when diagnosing a trigger.
- The service clears its own stale `power:inhibits` entry at startup and releases it during shutdown, but power management must also tolerate service failure.

## License

This project is licensed under the [Creative Commons Attribution-NonCommercial-ShareAlike 4.0 International License](LICENSE).

Made with ❤️ by the Librescoot community
