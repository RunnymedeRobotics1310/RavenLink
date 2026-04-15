package wpilog

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"
)

func TestWriteHeader_Minimal(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteHeader(&buf, ""); err != nil {
		t.Fatal(err)
	}
	got := buf.Bytes()

	// 6-byte magic + 2-byte version + 4-byte extra header length (0)
	want := []byte{
		'W', 'P', 'I', 'L', 'O', 'G', // magic
		0x00, 0x01, // version 1.0 LE (minor=0, major=1)
		0x00, 0x00, 0x00, 0x00, // extra header length = 0
	}
	if !bytes.Equal(got, want) {
		t.Errorf("header bytes:\n got  %x\n want %x", got, want)
	}
}

func TestWriteHeader_WithExtraHeader(t *testing.T) {
	var buf bytes.Buffer
	eh := `{"team":1310}`
	if err := WriteHeader(&buf, eh); err != nil {
		t.Fatal(err)
	}
	got := buf.Bytes()

	if len(got) != 12+len(eh) {
		t.Fatalf("length: got %d, want %d", len(got), 12+len(eh))
	}
	// Check extra header length field.
	ehLen := binary.LittleEndian.Uint32(got[8:12])
	if ehLen != uint32(len(eh)) {
		t.Errorf("extra header length: got %d, want %d", ehLen, len(eh))
	}
	if string(got[12:]) != eh {
		t.Errorf("extra header content: got %q, want %q", string(got[12:]), eh)
	}
}

func TestMinBytes(t *testing.T) {
	tests := []struct {
		v    uint64
		max  int
		want int
	}{
		{0, 4, 1},
		{1, 4, 1},
		{255, 4, 1},
		{256, 4, 2},
		{65535, 4, 2},
		{65536, 4, 3},
		{1_000_000, 8, 3}, // 0x0F4240 — fits in 3 bytes
		{0xFFFFFF, 8, 3},
		{0x1000000, 8, 4},
	}
	for _, tt := range tests {
		got := minBytes(tt.v, tt.max)
		if got != tt.want {
			t.Errorf("minBytes(%d, %d) = %d, want %d", tt.v, tt.max, got, tt.want)
		}
	}
}

func TestWriteDataRecord_SpecExample(t *testing.T) {
	// From the WPILog spec: int64 entry (ID=1) with value 3 at
	// timestamp 1,000,000 us.
	var buf bytes.Buffer

	// Encode value 3 as int64 LE.
	payload := make([]byte, 8)
	binary.LittleEndian.PutUint64(payload, 3)

	if err := WriteDataRecord(&buf, 1, 1_000_000, payload); err != nil {
		t.Fatal(err)
	}

	got := buf.Bytes()
	want := []byte{
		0x20,                         // bitfield: entry=1B, size=1B, ts=3B
		0x01,                         // entry ID = 1
		0x08,                         // payload size = 8
		0x40, 0x42, 0x0F,            // timestamp = 1,000,000 LE
		0x03, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, // value = 3
	}
	if !bytes.Equal(got, want) {
		t.Errorf("spec example:\n got  %x\n want %x", got, want)
	}
}

func TestMapNT4Type(t *testing.T) {
	tests := []struct {
		nt4  string
		want string
	}{
		{"int", "int64"},
		{"int[]", "int64[]"},
		{"float", "double"},
		{"float[]", "double[]"},
		{"double", "double"},
		{"boolean", "boolean"},
		{"string", "string"},
		{"raw", "raw"},
		{"boolean[]", "boolean[]"},
		{"double[]", "double[]"},
		{"string[]", "string[]"},
	}
	for _, tt := range tests {
		got := MapNT4Type(tt.nt4)
		if got != tt.want {
			t.Errorf("MapNT4Type(%q) = %q, want %q", tt.nt4, got, tt.want)
		}
	}
}

func TestEncodeValue_Boolean(t *testing.T) {
	b, err := EncodeValue("boolean", true)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b, []byte{0x01}) {
		t.Errorf("true: got %x", b)
	}
	b, err = EncodeValue("boolean", false)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b, []byte{0x00}) {
		t.Errorf("false: got %x", b)
	}
}

func TestEncodeValue_Double(t *testing.T) {
	b, err := EncodeValue("double", 3.14)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != 8 {
		t.Fatalf("len: got %d, want 8", len(b))
	}
	got := math.Float64frombits(binary.LittleEndian.Uint64(b))
	if got != 3.14 {
		t.Errorf("got %f, want 3.14", got)
	}
}

func TestEncodeValue_Int_FromFloat64(t *testing.T) {
	// JSON deserializes integers as float64.
	b, err := EncodeValue("int", float64(42))
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != 8 {
		t.Fatalf("len: got %d, want 8", len(b))
	}
	got := int64(binary.LittleEndian.Uint64(b))
	if got != 42 {
		t.Errorf("got %d, want 42", got)
	}
}

func TestEncodeValue_Int_Negative(t *testing.T) {
	b, err := EncodeValue("int", float64(-1))
	if err != nil {
		t.Fatal(err)
	}
	got := int64(binary.LittleEndian.Uint64(b))
	if got != -1 {
		t.Errorf("got %d, want -1", got)
	}
}

func TestEncodeValue_Float_PromotedToDouble(t *testing.T) {
	b, err := EncodeValue("float", float64(1.5))
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != 8 {
		t.Fatalf("float promoted to double should be 8 bytes, got %d", len(b))
	}
	got := math.Float64frombits(binary.LittleEndian.Uint64(b))
	if got != 1.5 {
		t.Errorf("got %f, want 1.5", got)
	}
}

func TestEncodeValue_String(t *testing.T) {
	b, err := EncodeValue("string", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "hello" {
		t.Errorf("got %q, want %q", string(b), "hello")
	}
}

func TestEncodeValue_Raw_Base64(t *testing.T) {
	// "aGVsbG8=" is base64 for "hello"
	b, err := EncodeValue("raw", "aGVsbG8=")
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "hello" {
		t.Errorf("got %q, want %q", string(b), "hello")
	}
}

func TestEncodeValue_BooleanArray(t *testing.T) {
	b, err := EncodeValue("boolean[]", []any{true, false, true})
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0x01, 0x00, 0x01}
	if !bytes.Equal(b, want) {
		t.Errorf("got %x, want %x", b, want)
	}
}

func TestEncodeValue_DoubleArray(t *testing.T) {
	b, err := EncodeValue("double[]", []any{1.0, 2.0})
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != 16 {
		t.Fatalf("len: got %d, want 16", len(b))
	}
	v0 := math.Float64frombits(binary.LittleEndian.Uint64(b[0:8]))
	v1 := math.Float64frombits(binary.LittleEndian.Uint64(b[8:16]))
	if v0 != 1.0 || v1 != 2.0 {
		t.Errorf("got [%f, %f], want [1.0, 2.0]", v0, v1)
	}
}

func TestEncodeValue_IntArray_FromFloat64(t *testing.T) {
	b, err := EncodeValue("int[]", []any{float64(10), float64(20)})
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != 16 {
		t.Fatalf("len: got %d, want 16", len(b))
	}
	v0 := int64(binary.LittleEndian.Uint64(b[0:8]))
	v1 := int64(binary.LittleEndian.Uint64(b[8:16]))
	if v0 != 10 || v1 != 20 {
		t.Errorf("got [%d, %d], want [10, 20]", v0, v1)
	}
}

func TestEncodeValue_StringArray(t *testing.T) {
	b, err := EncodeValue("string[]", []any{"ab", "c"})
	if err != nil {
		t.Fatal(err)
	}
	// Expected: u32(2) + u32(2) + "ab" + u32(1) + "c"
	// = 4 + 4 + 2 + 4 + 1 = 15 bytes
	if len(b) != 15 {
		t.Fatalf("len: got %d, want 15", len(b))
	}
	count := binary.LittleEndian.Uint32(b[0:4])
	if count != 2 {
		t.Errorf("count: got %d, want 2", count)
	}
	s0Len := binary.LittleEndian.Uint32(b[4:8])
	if s0Len != 2 || string(b[8:10]) != "ab" {
		t.Errorf("element 0: len=%d, val=%q", s0Len, string(b[8:10]))
	}
	s1Len := binary.LittleEndian.Uint32(b[10:14])
	if s1Len != 1 || string(b[14:15]) != "c" {
		t.Errorf("element 1: len=%d, val=%q", s1Len, string(b[14:15]))
	}
}

func TestExtractSessionID(t *testing.T) {
	tests := []struct {
		filename string
		want     string
	}{
		{"20260411T151030Z_0284a1cf.jsonl", "0284a1cf"},
		{"20260411T151030Z_abcdef01.jsonl", "abcdef01"},
		{"bad.jsonl", ""},
	}
	for _, tt := range tests {
		got := ExtractSessionID(tt.filename)
		if got != tt.want {
			t.Errorf("ExtractSessionID(%q) = %q, want %q", tt.filename, got, tt.want)
		}
	}
}

func TestExtractDate(t *testing.T) {
	tests := []struct {
		filename string
		want     string
	}{
		{"20260411T151030Z_0284a1cf.jsonl", "2026-04-11T15:10:30Z"},
		{"short", ""},
	}
	for _, tt := range tests {
		got := ExtractDate(tt.filename)
		if got != tt.want {
			t.Errorf("ExtractDate(%q) = %q, want %q", tt.filename, got, tt.want)
		}
	}
}

func TestConvert_EmptySession(t *testing.T) {
	jsonl := `{"type":"session_start","ts":1000.0,"team":1310,"robot_ip":"10.13.10.2","session_id":"aabbccdd"}
{"type":"session_end","ts":1001.0,"session_id":"aabbccdd","entries_written":0}
`
	out, err := Convert([]byte(jsonl), 1310, "aabbccdd")
	if err != nil {
		t.Fatal(err)
	}
	// Should be a valid WPILog with just a header (no Start/data records).
	if len(out) < 12 {
		t.Fatalf("output too short: %d bytes", len(out))
	}
	// Check magic.
	if string(out[:6]) != "WPILOG" {
		t.Errorf("magic: got %q", string(out[:6]))
	}
}

func TestConvert_WithDataAndMarkers(t *testing.T) {
	jsonl := `{"type":"session_start","ts":1000.0,"team":1310,"robot_ip":"10.13.10.2","session_id":"aabb"}
{"ts":1000.1,"server_ts":100000,"key":"/SmartDashboard/speed","type":"double","value":3.14}
{"ts":1000.2,"server_ts":200000,"key":"/SmartDashboard/speed","type":"double","value":6.28}
{"ts":1000.15,"server_ts":150000,"key":"/SmartDashboard/enabled","type":"boolean","value":true}
{"type":"match_start","ts":1000.05,"fms_enabled":true,"fms_auto":true,"fms_state":"Enabled Auto"}
{"type":"match_end","ts":1000.25,"fms_state":"Disabled"}
{"type":"session_end","ts":1001.0,"session_id":"aabb","entries_written":3}
`
	out, err := Convert([]byte(jsonl), 1310, "aabb")
	if err != nil {
		t.Fatal(err)
	}

	// Validate header.
	if string(out[:6]) != "WPILOG" {
		t.Fatalf("bad magic: %q", string(out[:6]))
	}

	// The output should contain:
	// - File header
	// - 2 Start records (speed:double, enabled:boolean)
	// - 1 Start record for /RavenLink/MatchEvent
	// - 3 data records
	// - 2 marker records
	// Total output should be non-trivially large.
	if len(out) < 100 {
		t.Errorf("output suspiciously small: %d bytes", len(out))
	}
}

func TestConvert_ServerTS_Zero_Fallback(t *testing.T) {
	jsonl := `{"type":"session_start","ts":1000.0,"team":1310,"robot_ip":"10.13.10.2","session_id":"ff00"}
{"ts":1000.5,"server_ts":0,"key":"/test","type":"double","value":1.0}
{"ts":1001.0,"server_ts":500000,"key":"/test","type":"double","value":2.0}
{"type":"session_end","ts":1002.0,"session_id":"ff00","entries_written":2}
`
	out, err := Convert([]byte(jsonl), 1310, "ff00")
	if err != nil {
		t.Fatal(err)
	}
	// Should succeed — the server_ts=0 entry uses wall-clock fallback.
	if len(out) < 12 {
		t.Fatalf("output too short: %d bytes", len(out))
	}
}

func TestConvert_IntType(t *testing.T) {
	// Verify int type entries round-trip through JSON float64.
	jsonl := `{"type":"session_start","ts":1000.0,"team":1310,"robot_ip":"10.13.10.2","session_id":"1234"}
{"ts":1000.1,"server_ts":100000,"key":"/counter","type":"int","value":42}
{"type":"session_end","ts":1001.0,"session_id":"1234","entries_written":1}
`
	out, err := Convert([]byte(jsonl), 1310, "1234")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) < 12 {
		t.Fatalf("output too short: %d bytes", len(out))
	}
}
