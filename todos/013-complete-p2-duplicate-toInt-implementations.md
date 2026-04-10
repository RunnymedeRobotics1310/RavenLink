---
status: pending
priority: p2
issue_id: 013
tags: [code-review, quality, duplication]
dependencies: []
---

# P2: Three copies of toInt() with inconsistent rounding behavior

## Problem Statement

Three packages each define their own `toInt(any) (int, bool)`:

1. **`internal/statemachine/fmsstate.go:106-133`** — uses `math.Round` for floats
2. **`internal/ntclient/protocol.go:152-205`** — truncates floats
3. **`cmd/ravenlink/main.go:365-393`** — truncates floats

Same input produces different results depending on which path hits it. For `FMSControlData` (always integer on the wire), this doesn't matter today — but it's a latent bug:

- If any NT topic ever delivers a value as float64 (which NT4 sometimes does), the three callers will disagree
- The main.go copy's truncation of `-0.5` → `0` instead of `-1` is the classic off-by-one
- Three copies = three places to fix when a new numeric type (e.g., `uint64`) needs support

## Findings

- **Locations**: 
  - `internal/statemachine/fmsstate.go:106-133`
  - `internal/ntclient/protocol.go:152-205`
  - `cmd/ravenlink/main.go:365-393`
- **Agent**: Code quality reviewer flagged as P2 #14

## Proposed Solutions

### Fix: Single shared helper in a new `internal/typeconv` package

Create `internal/typeconv/typeconv.go`:
```go
package typeconv

import "math"

// ToInt attempts to convert any numeric value to int using math.Round for floats.
// Returns (0, false) for non-numeric inputs.
func ToInt(v any) (int, bool) {
    switch n := v.(type) {
    case int:
        return n, true
    // ... all numeric types ...
    case float32:
        return int(math.Round(float64(n))), true
    case float64:
        return int(math.Round(n)), true
    }
    return 0, false
}
```

Delete all three existing copies, replace with `typeconv.ToInt`.

Effort: Small
Risk: Low — rounding behavior is consistent (math.Round is the safer default)

## Recommended Action

Apply the fix. Three copies → one source of truth.

## Technical Details

- New: `internal/typeconv/typeconv.go` + test file
- Modified:
  - `internal/statemachine/fmsstate.go` (remove local toInt, import typeconv)
  - `internal/ntclient/protocol.go` (same)
  - `cmd/ravenlink/main.go` (same)

## Acceptance Criteria

- [ ] Only one `ToInt` implementation in the codebase (`grep -r "func toInt\|func ToInt" --include="*.go"` returns one result)
- [ ] Tests cover all numeric types and rounding behavior
- [ ] All existing statemachine tests still pass
- [ ] `ToInt(-0.5)` returns `0` (consistent with banker's rounding via math.Round)
