package wpilog

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"math"
	"strings"
)

// MapNT4Type converts an NT4 type name to the corresponding WPILog
// type string. Unknown types are returned as-is.
func MapNT4Type(nt4Type string) string {
	switch nt4Type {
	case "int":
		return "int64"
	case "int[]":
		return "int64[]"
	case "float":
		return "double" // promote float32 → float64
	case "float[]":
		return "double[]"
	default:
		// boolean, double, string, raw, json, msgpack, protobuf,
		// boolean[], double[], string[] — names are identical.
		return nt4Type
	}
}

// EncodeValue converts a JSON-deserialized value into the WPILog
// binary payload for the given NT4 type. JSON numbers arrive as
// float64; integer types are cast accordingly.
func EncodeValue(nt4Type string, v any) ([]byte, error) {
	switch nt4Type {
	case "boolean":
		return encodeBoolean(v)
	case "double":
		return encodeDouble(v)
	case "int":
		return encodeInt64(v)
	case "float":
		return encodeDouble(v) // promoted to double
	case "string", "json":
		return encodeString(v)
	case "raw", "msgpack", "protobuf":
		return encodeRaw(v)
	case "boolean[]":
		return encodeBooleanArray(v)
	case "double[]":
		return encodeDoubleArray(v)
	case "int[]":
		return encodeInt64Array(v)
	case "float[]":
		return encodeDoubleArray(v) // promoted
	case "string[]":
		return encodeStringArray(v)
	default:
		// struct:*, structarray:*, and other extended types arrive as
		// raw binary. In JSONL they are base64-encoded (via ntlogger's
		// coerceValue). Treat them like raw.
		if strings.HasPrefix(nt4Type, "struct:") || strings.HasPrefix(nt4Type, "structarray:") {
			return encodeRaw(v)
		}
		return nil, fmt.Errorf("unsupported NT4 type %q", nt4Type)
	}
}

func encodeBoolean(v any) ([]byte, error) {
	b, ok := v.(bool)
	if !ok {
		return nil, fmt.Errorf("boolean: expected bool, got %T", v)
	}
	if b {
		return []byte{0x01}, nil
	}
	return []byte{0x00}, nil
}

func encodeDouble(v any) ([]byte, error) {
	f, ok := toFloat64(v)
	if !ok {
		return nil, fmt.Errorf("double: expected number, got %T", v)
	}
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, math.Float64bits(f))
	return buf, nil
}

func encodeInt64(v any) ([]byte, error) {
	f, ok := toFloat64(v)
	if !ok {
		return nil, fmt.Errorf("int64: expected number, got %T", v)
	}
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, uint64(int64(f)))
	return buf, nil
}

func encodeString(v any) ([]byte, error) {
	s, ok := v.(string)
	if !ok {
		return nil, fmt.Errorf("string: expected string, got %T", v)
	}
	return []byte(s), nil
}

func encodeRaw(v any) ([]byte, error) {
	s, ok := v.(string)
	if !ok {
		return nil, fmt.Errorf("raw: expected base64 string, got %T", v)
	}
	return base64.StdEncoding.DecodeString(s)
}

func encodeBooleanArray(v any) ([]byte, error) {
	arr, ok := toSlice(v)
	if !ok {
		return nil, fmt.Errorf("boolean[]: expected array, got %T", v)
	}
	buf := make([]byte, len(arr))
	for i, elem := range arr {
		b, ok := elem.(bool)
		if !ok {
			return nil, fmt.Errorf("boolean[%d]: expected bool, got %T", i, elem)
		}
		if b {
			buf[i] = 0x01
		}
	}
	return buf, nil
}

func encodeDoubleArray(v any) ([]byte, error) {
	arr, ok := toSlice(v)
	if !ok {
		return nil, fmt.Errorf("double[]: expected array, got %T", v)
	}
	buf := make([]byte, 8*len(arr))
	for i, elem := range arr {
		f, ok := toFloat64(elem)
		if !ok {
			return nil, fmt.Errorf("double[%d]: expected number, got %T", i, elem)
		}
		binary.LittleEndian.PutUint64(buf[i*8:], math.Float64bits(f))
	}
	return buf, nil
}

func encodeInt64Array(v any) ([]byte, error) {
	arr, ok := toSlice(v)
	if !ok {
		return nil, fmt.Errorf("int64[]: expected array, got %T", v)
	}
	buf := make([]byte, 8*len(arr))
	for i, elem := range arr {
		f, ok := toFloat64(elem)
		if !ok {
			return nil, fmt.Errorf("int64[%d]: expected number, got %T", i, elem)
		}
		binary.LittleEndian.PutUint64(buf[i*8:], uint64(int64(f)))
	}
	return buf, nil
}

func encodeStringArray(v any) ([]byte, error) {
	arr, ok := toSlice(v)
	if !ok {
		return nil, fmt.Errorf("string[]: expected array, got %T", v)
	}
	// WPILog string[]: u32 count, then (u32 len + UTF-8) per element.
	// Pre-compute total size.
	strs := make([]string, len(arr))
	totalSize := 4 // count
	for i, elem := range arr {
		s, ok := elem.(string)
		if !ok {
			return nil, fmt.Errorf("string[%d]: expected string, got %T", i, elem)
		}
		strs[i] = s
		totalSize += 4 + len(s)
	}

	buf := make([]byte, totalSize)
	binary.LittleEndian.PutUint32(buf[0:], uint32(len(strs)))
	off := 4
	for _, s := range strs {
		binary.LittleEndian.PutUint32(buf[off:], uint32(len(s)))
		off += 4
		copy(buf[off:], s)
		off += len(s)
	}
	return buf, nil
}

// toFloat64 extracts a float64 from a JSON-decoded numeric value.
func toFloat64(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	default:
		return 0, false
	}
}

// toSlice extracts a []any from a JSON-decoded array value.
func toSlice(v any) ([]any, bool) {
	arr, ok := v.([]any)
	return arr, ok
}
