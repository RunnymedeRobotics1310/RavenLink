// Package typeconv provides type conversion helpers for dynamically-typed
// values (typically from NetworkTables or JSON decoding).
package typeconv

import "math"

// ToInt attempts to convert any numeric value to int, using math.Round for
// floating-point inputs. Returns (0, false) if the value is not a numeric type.
func ToInt(v any) (int, bool) {
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
	case uint:
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
	}
	return 0, false
}

// ToInt64 attempts to convert any numeric value to int64, using math.Round
// for floating-point inputs. Returns (0, false) if not numeric.
func ToInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int8:
		return int64(n), true
	case int16:
		return int64(n), true
	case int32:
		return int64(n), true
	case int64:
		return n, true
	case uint:
		return int64(n), true
	case uint8:
		return int64(n), true
	case uint16:
		return int64(n), true
	case uint32:
		return int64(n), true
	case uint64:
		return int64(n), true
	case float32:
		return int64(math.Round(float64(n))), true
	case float64:
		return int64(math.Round(n)), true
	}
	return 0, false
}
