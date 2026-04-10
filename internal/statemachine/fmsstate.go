package statemachine

import (
	"fmt"
	"math"
)

// FMSState represents the parsed FRC Driver Station / FMS control word.
type FMSState struct {
	Enabled     bool
	AutoMode    bool
	TestMode    bool
	EStop       bool
	FMSAttached bool
	DSAttached  bool
	Raw         int
}

// FMSStateFromRaw parses the 6-bit FMS control bitmask into an FMSState.
//
// Bit layout:
//
//	0x01  Enabled
//	0x02  Auto mode
//	0x04  Test mode
//	0x08  Emergency stop
//	0x10  FMS attached
//	0x20  DS attached
func FMSStateFromRaw(value int) FMSState {
	return FMSState{
		Enabled:     value&0x01 != 0,
		AutoMode:    value&0x02 != 0,
		TestMode:    value&0x04 != 0,
		EStop:       value&0x08 != 0,
		FMSAttached: value&0x10 != 0,
		DSAttached:  value&0x20 != 0,
		Raw:         value,
	}
}

// FMSStateDisconnected returns an FMSState representing a NetworkTables
// disconnect (Raw = -1, all flags false).
func FMSStateDisconnected() FMSState {
	return FMSState{Raw: -1}
}

// String returns a human-readable representation of the FMS state.
func (s FMSState) String() string {
	if s.Raw < 0 {
		return "FMSState(DISCONNECTED)"
	}

	var flags []string
	if s.Enabled {
		flags = append(flags, "ENABLED")
	}
	if s.AutoMode {
		flags = append(flags, "AUTO")
	}
	if s.TestMode {
		flags = append(flags, "TEST")
	}
	if s.EStop {
		flags = append(flags, "ESTOP")
	}
	if s.FMSAttached {
		flags = append(flags, "FMS")
	}
	if s.DSAttached {
		flags = append(flags, "DS")
	}

	label := "NONE"
	if len(flags) > 0 {
		for i, f := range flags {
			if i == 0 {
				label = f
			} else {
				label += " | " + f
			}
		}
	}
	return fmt.Sprintf("FMSState(%s, raw=0x%02x)", label, s.Raw)
}

// FMSControlDataKey is the NetworkTables key for the FMS control word.
const FMSControlDataKey = "/FMSInfo/FMSControlData"

// ExtractFMSState extracts an FMSState from a map of topic name to value,
// as produced by the NT4 client. If the FMSControlData key is missing or
// the value is not numeric, FMSStateDisconnected() is returned.
func ExtractFMSState(values map[string]any) FMSState {
	v, ok := values[FMSControlDataKey]
	if !ok {
		return FMSStateDisconnected()
	}

	raw, ok := toInt(v)
	if !ok {
		return FMSStateDisconnected()
	}
	return FMSStateFromRaw(raw)
}

// toInt converts various numeric types to int.
func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int8:
		return int(n), true
	case int16:
		return int(n), true
	case int32:
		return int(n), true
	case int64:
		return int(n), true
	case uint8:
		return int(n), true
	case uint16:
		return int(n), true
	case uint32:
		return int(n), true
	case uint64:
		return int(n), true
	case float32:
		return int(math.Round(float64(n))), true
	case float64:
		return int(math.Round(n)), true
	default:
		return 0, false
	}
}
