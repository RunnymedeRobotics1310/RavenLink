# RavenLink

Automatically starts and stops OBS Studio recording based on FRC match state, **and** captures all NetworkTables data for post-match analysis. Runs on the Driver Station laptop, reads match status from the robot via NetworkTables, controls OBS via its WebSocket API, and uploads telemetry to RavenBrain.

When the FMS enables your robot for a match, recording starts and NT data logging begins. When the match ends, recording stops after a short delay. All telemetry data is stored locally and forwarded to RavenBrain when internet is available. No more forgetting to hit record, no more lost telemetry.

## Features

- **OBS Recording Control** — Auto start/stop OBS recording based on FMS match state
- **NT Data Collection** — Subscribe to configurable NetworkTables paths, log all value changes to JSONL
- **Store & Forward** — Data saved locally first, uploaded to RavenBrain when connectivity available
- **Web Dashboard** — Live status, log viewer, and config editor at `http://localhost:8080`
- **System Tray** — At-a-glance green/yellow/red status icon with right-click menu
- **Launch on Login** — Starts automatically with Windows (or macOS), minimized to tray

## Prerequisites

- **OBS Studio 28+** with the WebSocket server enabled
- **Windows 10/11** (Driver Station laptop) for competition use
- **Python 3.11+** if running from source (or just use the `.exe`)

## OBS Setup

1. Open OBS Studio
2. Go to **Tools > WebSocket Server Settings**
3. Check **Enable WebSocket Server**
4. Set a **Server Port** (default: `4455`)
5. Optionally set a **Server Password**
6. Click **Apply**

## Usage

### From the `.exe`

```
ravenlink.exe --team 1310
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
| `--poll-interval` | `0.05` | How often to check match state (seconds) |
| `--log-level` | `INFO` | Log verbosity: DEBUG, INFO, WARNING, ERROR |
| `--auto-teleop-gap` | `5` | Max disabled seconds between auto/teleop before stopping |
| `--nt-disconnect-grace` | `15` | Seconds to wait before treating NT disconnect as match over |
| `--nt-paths` | `/SmartDashboard/, /Shuffleboard/` | NT path prefixes to subscribe to (comma-separated) |
| `--data-dir` | `./data` | Local directory for JSONL telemetry files |
| `--record-trigger` | `fms` | When to start recording: `fms` (competition only), `auto` (auto mode — catches DS Practice), `any` (any enable) |
| `--ravenbrain-url` | *(empty)* | RavenBrain server URL (empty = local-only mode) |
| `--ravenbrain-username` | *(empty)* | RavenBrain service account username |
| `--ravenbrain-password` | *(empty)* | RavenBrain service account password |
| `--no-launch-on-login` | false | Disable automatic launch on login |
| `--minimized` | false | Start minimized to system tray |

### Config file

Instead of passing flags every time, create a `config.ini` next to the executable (or edit via the web dashboard):

```ini
[bridge]
team = 1310
obs_host = localhost
obs_port = 4455
obs_password =
stop_delay = 10
poll_interval = 0.05
log_level = INFO
record_trigger = fms
launch_on_login = true

[telemetry]
nt_paths = /SmartDashboard/, /Shuffleboard/
data_dir = ./data
retention_days = 30

[ravenbrain]
url = https://ravenbrain.team1310.ca
username = telemetry-agent
password = your-password-here
batch_size = 500
upload_interval = 10

[dashboard]
enabled = true
port = 8080
```

CLI flags override config file values. The web dashboard at `http://localhost:8080` allows editing config with live preview.

## How It Works

### Match Recording

The bridge runs a state machine with a configurable trigger (`record_trigger`):

| Mode | Trigger | Use case |
|------|---------|----------|
| `fms` | FMS attached + enabled | Competition matches (default) |
| `auto` | Auto mode + enabled | DS Practice button, manual auto enables |
| `any` | Any enable | Any robot enable triggers recording |

States:
1. **IDLE** — Waiting. Monitoring NetworkTables for the configured trigger condition.
2. **RECORDING (auto)** — Trigger condition met. OBS recording started, `match_start` marker written.
3. **RECORDING (teleop)** — Robot transitions to teleop. Recording continues.
4. **STOP_PENDING** — Robot disabled. `match_end` marker written. Waits `stop_delay` seconds then stops OBS recording.

The brief disabled gap between auto and teleop (up to `auto_teleop_gap` seconds) is tolerated in all modes so recording isn't accidentally split.

### NT Data Collection

All NetworkTables value changes under configured path prefixes are logged to JSONL files:
- One file per connectivity session (robot connect → disconnect)
- Match start/end markers with FMS metadata embedded in the data stream
- `/FMSInfo/*` is always subscribed for match association

### Store & Forward

- Data is always written locally to `data_dir/pending/`
- When RavenBrain is reachable, completed files are uploaded in batches
- Authenticates via JWT (`POST /login` with service account credentials, auto-renews before expiry)
- Upload progress tracked server-side (`uploadedCount`) — idempotent on retry, no duplicate data
- Uploaded files are moved to `data_dir/uploaded/` and pruned after `retention_days`

## Web Dashboard

Access at `http://localhost:8080` when the bridge is running:

- **Status tab** — Live connection status, match state, telemetry stats, upload progress
- **Logs tab** — Recent log output
- **Config tab** — Edit all settings, save to `config.ini`, hot-reload supported fields

## Troubleshooting

**OBS not detected:**
- Ensure OBS is running and WebSocket server is enabled
- Check the port matches (`--obs-port`)
- If you set a password in OBS, pass it with `--obs-password`

**NetworkTables not connecting:**
- Verify your team number is correct (`--team`)
- Ensure the DS laptop can reach the robot at `10.TE.AM.2`
- Check that your firewall allows port 5810

**Recording doesn't start:**
- Check your `record_trigger` setting — `fms` (default) requires FMS to be attached
- For home practice, set `record_trigger = auto` (use DS Practice button) or `record_trigger = any`
- In `auto` mode, you must enable in auto mode — plain teleop enable won't trigger

**Data not uploading:**
- Check that `ravenbrain_url` is set in config
- Verify `username` and `password` are correct for the `telemetry-agent` service account
- Check the dashboard upload status for error messages (auth errors, connection errors)
- If you see repeated 401 errors, the password may have been changed on the server

## Building & Deploying on Windows

### Prerequisites

1. Install **Python 3.11+** from [python.org](https://www.python.org/downloads/) — check "Add Python to PATH" during install
2. Install **OBS Studio 28+** from [obsproject.com](https://obsproject.com/)
3. Install **Git** from [git-scm.com](https://git-scm.com/) (optional, for cloning the repo)

### Build the `.exe`

Open a Command Prompt or PowerShell:

```powershell
# Clone the repo (or copy the folder to the DS laptop)
git clone https://github.com/RunnymedeRobotics1310/RavenLink.git
cd RavenLink

# Create a virtual environment
python -m venv venv
venv\Scripts\activate

# Install dependencies
pip install -r requirements.txt
pip install pyinstaller

# Build single-file exe
pyinstaller build.spec

# Output: dist\ravenlink.exe
```

### Deploy to the Driver Station Laptop

1. Copy `dist\ravenlink.exe` to a permanent location (e.g., `C:\FRC\ravenlink\`)
2. Create a `config.ini` in the same folder:

```ini
[bridge]
team = 1310
obs_host = localhost
obs_port = 4455
obs_password =
launch_on_login = true

[telemetry]
nt_paths = /SmartDashboard/, /Shuffleboard/
data_dir = ./data

[ravenbrain]
url = https://ravenbrain.team1310.ca
username = telemetry-agent
password = your-password-here

[dashboard]
enabled = true
port = 8080
```

3. Run it once to verify and register auto-start:

```powershell
cd C:\FRC\ravenlink
.\ravenlink.exe --team 1310
```

4. The bridge will:
   - Register itself to launch on login (via Windows Registry `HKCU\...\Run`)
   - Start the web dashboard at `http://localhost:8080`
   - Show a system tray icon (green/yellow/red)
   - Begin capturing NT data when the robot connects

### Competition Day Checklist

1. Turn on the DS laptop — the bridge starts automatically (system tray icon appears)
2. Open OBS Studio — ensure WebSocket server is enabled
3. Verify via the dashboard (`http://localhost:8080`):
   - NT: Connected (when robot is on)
   - OBS: Connected
4. The bridge handles everything else automatically:
   - Starts/stops OBS recording per match
   - Logs all NT data to local JSONL files
   - Uploads to RavenBrain when WiFi is available (pit, hotel, etc.)

### Running from Source (without building `.exe`)

If you prefer not to build:

```powershell
cd RavenLink
python -m venv venv
venv\Scripts\activate
pip install -r requirements.txt
python -m src.main --team 1310
```

### Updating

```powershell
cd RavenLink
git pull
venv\Scripts\activate
pip install -r requirements.txt
pyinstaller build.spec
# Copy dist\ravenlink.exe to your deployment folder
```

## License

MIT
