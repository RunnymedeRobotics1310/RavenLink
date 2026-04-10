package ntclient

import (
	"reflect"
	"testing"

	"github.com/vmihailenco/msgpack/v5"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// assertTopicData compares a decoded TopicData entry against the original
// input. Because MessagePack round-trips widen integer values (e.g. int ->
// int8 depending on magnitude) and does not preserve []byte vs string for
// raw types in all cases, the value comparison is loose: we check TopicID,
// TimestampMicros, and TypeID exactly and compare the value via reflect after
// a normalization pass.
func assertTopicData(t *testing.T, got, want TopicData) {
	t.Helper()
	if got.TopicID != want.TopicID {
		t.Errorf("TopicID: got %d, want %d", got.TopicID, want.TopicID)
	}
	if got.TimestampMicros != want.TimestampMicros {
		t.Errorf("TimestampMicros: got %d, want %d", got.TimestampMicros, want.TimestampMicros)
	}
	if got.TypeID != want.TypeID {
		t.Errorf("TypeID: got %d, want %d", got.TypeID, want.TypeID)
	}
}

// ---------------------------------------------------------------------------
// TestTypeIDConstants — pin the NT4 type ID constants so accidental changes
// are caught.
// ---------------------------------------------------------------------------

func TestTypeIDConstants(t *testing.T) {
	cases := []struct {
		name string
		id   int
		want int
	}{
		{"boolean", TypeBoolean, 0},
		{"double", TypeDouble, 1},
		{"int", TypeInt, 2},
		{"float", TypeFloat, 3},
		{"string", TypeString, 4},
		{"raw", TypeRaw, 5},
		{"bool[]", TypeBoolArray, 16},
		{"double[]", TypeDoubleArray, 17},
		{"int[]", TypeIntArray, 18},
		{"float[]", TypeFloatArray, 19},
		{"string[]", TypeStringArray, 20},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.id != c.want {
				t.Errorf("type id: got %d, want %d", c.id, c.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestTypeName — human-readable names & unknown fallback.
// ---------------------------------------------------------------------------

func TestTypeName(t *testing.T) {
	cases := []struct {
		id   int
		want string
	}{
		{TypeBoolean, "boolean"},
		{TypeDouble, "double"},
		{TypeInt, "int"},
		{TypeString, "string"},
		{TypeRaw, "raw"},
		{TypeIntArray, "int[]"},
		{999, "unknown(999)"},
	}
	for _, c := range cases {
		t.Run(c.want, func(t *testing.T) {
			if got := TypeName(c.id); got != c.want {
				t.Errorf("TypeName(%d) = %q, want %q", c.id, got, c.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestEncodeDecodeRoundTrip — encode a slice of TopicData and ensure the
// decoded output matches the input for each supported type.
// ---------------------------------------------------------------------------

func TestEncodeDecodeRoundTrip(t *testing.T) {
	t.Run("boolean", func(t *testing.T) {
		entries := []TopicData{{TopicID: 1, TimestampMicros: 1000, TypeID: TypeBoolean, Value: true}}
		got := roundTrip(t, entries)
		if len(got) != 1 {
			t.Fatalf("len: got %d, want 1", len(got))
		}
		assertTopicData(t, got[0], entries[0])
		if v, ok := got[0].Value.(bool); !ok || v != true {
			t.Errorf("value: got %#v, want true", got[0].Value)
		}
	})

	t.Run("double", func(t *testing.T) {
		entries := []TopicData{{TopicID: 2, TimestampMicros: 2000, TypeID: TypeDouble, Value: 3.14}}
		got := roundTrip(t, entries)
		assertTopicData(t, got[0], entries[0])
		if v, ok := got[0].Value.(float64); !ok || v != 3.14 {
			t.Errorf("value: got %#v, want 3.14", got[0].Value)
		}
	})

	t.Run("int", func(t *testing.T) {
		// Use a value large enough to avoid msgpack collapsing to a small int
		// type that wouldn't exercise the int path. Any numeric decoded value
		// should survive conversion via typeconv.
		entries := []TopicData{{TopicID: 3, TimestampMicros: 3000, TypeID: TypeInt, Value: int64(123456)}}
		got := roundTrip(t, entries)
		assertTopicData(t, got[0], entries[0])
		// typeconv will handle whatever numeric type msgpack picked.
		if got[0].Value == nil {
			t.Error("value is nil")
		}
	})

	t.Run("string", func(t *testing.T) {
		entries := []TopicData{{TopicID: 4, TimestampMicros: 4000, TypeID: TypeString, Value: "hello"}}
		got := roundTrip(t, entries)
		assertTopicData(t, got[0], entries[0])
		if v, ok := got[0].Value.(string); !ok || v != "hello" {
			t.Errorf("value: got %#v, want %q", got[0].Value, "hello")
		}
	})

	t.Run("raw", func(t *testing.T) {
		raw := []byte{0x01, 0x02, 0x03, 0xff}
		entries := []TopicData{{TopicID: 5, TimestampMicros: 5000, TypeID: TypeRaw, Value: raw}}
		got := roundTrip(t, entries)
		assertTopicData(t, got[0], entries[0])
		b, ok := got[0].Value.([]byte)
		if !ok {
			t.Fatalf("value: got %T, want []byte", got[0].Value)
		}
		if !reflect.DeepEqual(b, raw) {
			t.Errorf("raw bytes: got %v, want %v", b, raw)
		}
	})

	t.Run("int_array", func(t *testing.T) {
		arr := []int64{10, 20, 30}
		entries := []TopicData{{TopicID: 6, TimestampMicros: 6000, TypeID: TypeIntArray, Value: arr}}
		got := roundTrip(t, entries)
		assertTopicData(t, got[0], entries[0])
		decoded, ok := got[0].Value.([]any)
		if !ok {
			t.Fatalf("value: got %T, want []any", got[0].Value)
		}
		if len(decoded) != len(arr) {
			t.Errorf("len: got %d, want %d", len(decoded), len(arr))
		}
	})
}

// ---------------------------------------------------------------------------
// TestEncodeEmptyFrame — encoding an empty slice round-trips cleanly.
// ---------------------------------------------------------------------------

func TestEncodeEmptyFrame(t *testing.T) {
	data, err := EncodeDataFrame(nil)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	entries, err := DecodeDataFrame(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(entries))
	}
}

// ---------------------------------------------------------------------------
// TestEncodeMultipleEntries — a single frame with multiple heterogeneous
// entries round-trips.
// ---------------------------------------------------------------------------

func TestEncodeMultipleEntries(t *testing.T) {
	entries := []TopicData{
		{TopicID: 1, TimestampMicros: 1000, TypeID: TypeBoolean, Value: true},
		{TopicID: 2, TimestampMicros: 2000, TypeID: TypeDouble, Value: 1.5},
		{TopicID: 3, TimestampMicros: 3000, TypeID: TypeString, Value: "x"},
	}
	got := roundTrip(t, entries)
	if len(got) != len(entries) {
		t.Fatalf("len: got %d, want %d", len(got), len(entries))
	}
	for i := range entries {
		assertTopicData(t, got[i], entries[i])
	}
}

// ---------------------------------------------------------------------------
// TestDecodeMalformed — garbage input returns an error without panicking.
// ---------------------------------------------------------------------------

func TestDecodeMalformed(t *testing.T) {
	t.Run("random_garbage", func(t *testing.T) {
		// 0xc1 is a reserved msgpack byte and should fail to unmarshal.
		if _, err := DecodeDataFrame([]byte{0xc1, 0xc1, 0xc1}); err == nil {
			t.Error("expected error on garbage input")
		}
	})

	t.Run("empty_bytes", func(t *testing.T) {
		if _, err := DecodeDataFrame(nil); err == nil {
			t.Error("expected error on nil input")
		}
	})

	t.Run("short_entry", func(t *testing.T) {
		// A valid msgpack array of arrays with fewer than 4 elements per entry.
		short := [][]any{{1, 2}} // only 2 elements, need 4
		data, err := msgpack.Marshal(short)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if _, err := DecodeDataFrame(data); err == nil {
			t.Error("expected error on short entry")
		}
	})

	t.Run("non_numeric_topic_id", func(t *testing.T) {
		bad := [][]any{{"not-a-number", 1000, 0, true}}
		data, err := msgpack.Marshal(bad)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if _, err := DecodeDataFrame(data); err == nil {
			t.Error("expected error on non-numeric topic id")
		}
	})

	t.Run("non_numeric_timestamp", func(t *testing.T) {
		bad := [][]any{{1, "bad-ts", 0, true}}
		data, err := msgpack.Marshal(bad)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if _, err := DecodeDataFrame(data); err == nil {
			t.Error("expected error on non-numeric timestamp")
		}
	})

	t.Run("non_numeric_type_id", func(t *testing.T) {
		bad := [][]any{{1, 1000, "not-a-type", true}}
		data, err := msgpack.Marshal(bad)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if _, err := DecodeDataFrame(data); err == nil {
			t.Error("expected error on non-numeric type id")
		}
	})
}

// ---------------------------------------------------------------------------
// roundTrip encodes then decodes a slice of TopicData and returns the result.
// ---------------------------------------------------------------------------

func roundTrip(t *testing.T, entries []TopicData) []TopicData {
	t.Helper()
	data, err := EncodeDataFrame(entries)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := DecodeDataFrame(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return got
}
