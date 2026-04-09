# FRC OBS Recording Bridge

Automatically starts and stops OBS Studio recording based on FRC match state. Runs on the Driver Station laptop, reads match status from the robot via NetworkTables, and controls OBS via its WebSocket API.

When the FMS enables your robot for a match, recording starts. When the match ends, recording stops after a short delay. No more forgetting to hit record.

## Prerequisites

- **OBS Studio 28+** with the WebSocket server enabled
- **Windows 10/11** (Driver Station laptop) for competition use
- **Python 3.11+** if running from source (or just use the `.exe`)

## OBS Setup

1. Open OBS Studio
2. Go to **Tools → WebSocket Server Settings**
3. Check **Enable WebSocket Server**
4. Set a **Server Port** (default: `4455`)
5. Optionally set a **Server Password**
6. Click **Apply**

## Usage

### From the `.exe`

```
frc-obs-bridge.exe --team 1310
```

### From source

```bash
pip install -r requirements.txt
python -m src.main --team 1310
```

### All options

| Flag | Default | Description |
|------|---------|-------------|
| `--team` | *(required)* | Team number — derives robot IP `10.TE.AM.2` |
| `--obs-host` | `localhost` | OBS WebSocket host |
| `--obs-port` | `4455` | OBS WebSocket port |
| `--obs-password` | *(empty)* | OBS WebSocket password |
| `--stop-delay` | `10` | Seconds after match end before stopping recording |
| `--poll-interval` | `1.0` | How often to check match state (seconds) |
| `--log-level` | `INFO` | Log verbosity: DEBUG, INFO, WARNING, ERROR |
| `--auto-teleop-gap` | `5` | Max disabled seconds between auto/teleop before stopping |
| `--nt-disconnect-grace` | `15` | Seconds to wait before treating NT disconnect as match over |

### Config file

Instead of passing flags every time, create a `config.ini` next to the executable:

```ini
[bridge]
team = 1310
obs_host = localhost
obs_port = 4455
obs_password =
stop_delay = 10
poll_interval = 0.1
log_level = INFO
```

CLI flags override config file values.

## How It Works

The bridge runs a simple state machine:

1. **IDLE** — Waiting for a match. Monitoring NetworkTables for FMS state.
2. **RECORDING (auto)** — FMS attached + robot enabled in auto mode. OBS recording started.
3. **RECORDING (teleop)** — Robot transitions to teleop. Recording continues uninterrupted.
4. **STOP_PENDING** — Match ended (robot disabled). Waits 10 seconds then stops recording.

The brief disabled period between autonomous and teleoperated modes is tolerated (up to 5 seconds by default) so recording isn't accidentally split.

## Troubleshooting

**OBS not detected:**
- Ensure OBS is running and WebSocket server is enabled (Tools → WebSocket Server Settings)
- Check the port matches (`--obs-port`)
- If you set a password in OBS, pass it with `--obs-password`

**NetworkTables not connecting:**
- Verify your team number is correct (`--team`)
- Ensure the DS laptop can reach the robot at `10.TE.AM.2`
- Check that your firewall allows port 5810

**Recording doesn't start:**
- The bridge only starts recording when FMS is attached (competition/practice matches connected to the field)
- Practice at home without FMS won't trigger recording — this is intentional
- Check the console logs for state transitions

**Recording stops unexpectedly:**
- Check for NT disconnections in the logs
- Increase `--nt-disconnect-grace` if your connection is flaky
- Increase `--auto-teleop-gap` if the auto→teleop transition is taking longer than expected

## Building from Source

```bash
# Install dependencies
pip install -r requirements.txt
pip install pyinstaller

# Build single-file exe
pyinstaller build.spec

# Output: dist/frc-obs-bridge.exe
```

## License

MIT
