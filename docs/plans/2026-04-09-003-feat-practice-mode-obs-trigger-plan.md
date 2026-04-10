---
title: "feat: Configurable recording trigger mode (fms/auto/any)"
type: feat
status: active
date: 2026-04-09
---

# feat: Configurable Recording Trigger Mode

## Overview

Currently, OBS recording only starts when FMS is attached + robot is enabled. This prevents testing during home practice. Add a `record_trigger` config option with three modes:

- **`fms`** (default) — Only start recording when FMS is attached. Current behavior.
- **`auto`** — Start recording when robot enters auto mode (enabled + auto_mode), with or without FMS. Catches DS Practice button and manual auto enables.
- **`any`** — Start recording on any robot enable, regardless of mode or FMS.

## Proposed Solution

### State machine changes (`src/state_machine.py`)

Add `record_trigger: str = "fms"` to `MatchStateMachine.__init__`.

**IDLE → RECORDING_AUTO transition (line 101):**

```python
# Current:
if fms.fms_attached and fms.enabled:

# New:
if self._should_start_recording(fms):
```

```python
def _should_start_recording(self, fms: FMSState) -> bool:
    if not fms.enabled:
        return False
    if self._record_trigger == "fms":
        return fms.fms_attached
    elif self._record_trigger == "auto":
        return fms.auto_mode
    else:  # "any"
        return True
```

**Stop behavior in `auto`/`any` modes:** In `fms` mode, the stop path relies on the match lifecycle (disable after teleop → STOP_PENDING). In `auto`/`any` mode without FMS, there's no guaranteed match structure. The rule: **any disable while recording triggers STOP_PENDING** (with the same `stop_delay`). The auto-teleop gap tolerance still applies — if the DS Practice button runs auto→brief disable→teleop, the gap is tolerated and recording continues seamlessly. But a sustained disable (longer than `auto_teleop_gap`) triggers the stop, just like in FMS mode.

This means the existing RECORDING_AUTO and RECORDING_TELEOP disable logic already handles this correctly — the auto-teleop gap timer catches short disables, and anything longer enters STOP_PENDING. No change needed to the stop path.

**FMS detach handler (line 84):** Only fire when `record_trigger == "fms"`. In `auto`/`any` modes, FMS is never expected so detach is irrelevant.

**STOP_PENDING re-enable (line 141):** Currently requires `fms.fms_attached`. Change to use `_should_start_recording(fms)` instead — if the trigger condition is still met, resume recording.

### Config changes (`src/config.py`)

Add `record_trigger: str = "fms"` to Config dataclass. Add `--record-trigger` CLI arg with choices `["fms", "auto", "any"]`. Add to `[bridge]` INI section. Add to hot-reloadable fields.

### Wiring (`src/main.py`)

Pass `config.record_trigger` to `MatchStateMachine(...)`.

## Files to Modify

| File | Change |
|------|--------|
| `src/state_machine.py:30-36` | Add `record_trigger: str = "fms"` param |
| `src/state_machine.py:99-106` | Replace hardcoded FMS check with `_should_start_recording()` |
| `src/state_machine.py:84` | Guard FMS detach with `record_trigger == "fms"` |
| `src/state_machine.py:141` | Relax re-enable check based on trigger mode |
| `src/config.py` | Add `record_trigger` field, CLI arg, INI key, hot-reload |
| `src/main.py` | Pass `record_trigger` to state machine |
| `config.ini.example` | Add `record_trigger = fms` |
| `tests/test_state_machine.py` | Add `TestRecordTriggerAuto` and `TestRecordTriggerAny` classes |

## Acceptance Criteria

- [ ] `record_trigger = fms` — existing behavior unchanged, all existing tests pass
- [ ] `record_trigger = auto` — recording starts on auto+enabled without FMS (DS Practice button)
- [ ] `record_trigger = any` — recording starts on any enable without FMS
- [ ] FMS detach handling only fires in `fms` mode
- [ ] Re-enable during STOP_PENDING works in all modes
- [ ] Config editable via dashboard, hot-reloadable
- [ ] Full match lifecycle works in all three modes
- [ ] In `auto`/`any` mode, disable stops recording after `stop_delay` (same as FMS mode)
- [ ] In `auto`/`any` mode, DS Practice auto→teleop gap is tolerated (recording not split)
