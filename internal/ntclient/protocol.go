package ntclient

import (
	"bytes"
	"fmt"
	"io"

	"github.com/RunnymedeRobotics1310/RavenLink/internal/typeconv"
	"github.com/vmihailenco/msgpack/v5"
)

// NT4 type ID constants as defined by the NetworkTables 4 protocol.
const (
	TypeBoolean     = 0
	TypeDouble      = 1
	TypeInt         = 2
	TypeFloat       = 3
	TypeString      = 4
	TypeRaw         = 5
	TypeBoolArray   = 16
	TypeDoubleArray = 17
	TypeIntArray    = 18
	TypeFloatArray  = 19
	TypeStringArray = 20
)

// typeIDToName maps NT4 type IDs to human-readable type names.
var typeIDToName = map[int]string{
	TypeBoolean:     "boolean",
	TypeDouble:      "double",
	TypeInt:         "int",
	TypeFloat:       "float",
	TypeString:      "string",
	TypeRaw:         "raw",
	TypeBoolArray:   "boolean[]",
	TypeDoubleArray: "double[]",
	TypeIntArray:    "int[]",
	TypeFloatArray:  "float[]",
	TypeStringArray: "string[]",
}

// TypeName returns the human-readable name for a type ID, or "unknown(N)".
func TypeName(id int) string {
	if name, ok := typeIDToName[id]; ok {
		return name
	}
	return fmt.Sprintf("unknown(%d)", id)
}

// SubscribeMessage represents a client-to-server subscribe request.
type SubscribeMessage struct {
	Method string          `json:"method"`
	Params SubscribeParams `json:"params"`
}

// SubscribeParams holds the parameters for a subscribe message.
type SubscribeParams struct {
	Topics  []string         `json:"topics"`
	SubUID  int              `json:"subuid"`
	Options SubscribeOptions `json:"options"`
}

// SubscribeOptions holds subscription options.
type SubscribeOptions struct {
	All    bool `json:"all"`
	Prefix bool `json:"prefix"`
}

// AnnounceMessage represents a server-to-client announce notification.
type AnnounceMessage struct {
	Method string         `json:"method"`
	Params AnnounceParams `json:"params"`
}

// AnnounceParams holds the parameters for an announce message.
type AnnounceParams struct {
	Name       string         `json:"name"`
	ID         int            `json:"id"`
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties"`
}

// UnannounceMessage represents a server-to-client unannounce notification.
type UnannounceMessage struct {
	Method string           `json:"method"`
	Params UnannounceParams `json:"params"`
}

// UnannounceParams holds the parameters for an unannounce message.
type UnannounceParams struct {
	Name string `json:"name"`
	ID   int    `json:"id"`
}

// TopicData represents a single decoded data frame entry from a binary
// MessagePack frame. Each entry is a 4-element array:
// [topicID, timestampMicros, typeID, value].
type TopicData struct {
	TopicID         int
	TimestampMicros int64
	TypeID          int
	Value           any
}

// EncodeDataFrame encodes a slice of TopicData into a MessagePack binary
// payload suitable for sending as a WebSocket binary frame.
//
// NT4 binary frames are a sequence of concatenated 4-element MessagePack
// arrays, NOT a single top-level array. See the NT4 protocol specification:
// https://github.com/wpilibsuite/allwpilib/blob/main/ntcore/doc/networktables4.adoc
func EncodeDataFrame(entries []TopicData) ([]byte, error) {
	var buf bytes.Buffer
	enc := msgpack.NewEncoder(&buf)
	for _, e := range entries {
		arr := []any{e.TopicID, e.TimestampMicros, e.TypeID, e.Value}
		if err := enc.Encode(arr); err != nil {
			return nil, fmt.Errorf("msgpack encode: %w", err)
		}
	}
	return buf.Bytes(), nil
}

// DecodeDataFrame decodes a MessagePack binary payload into a slice of
// TopicData entries.
//
// NT4 binary frames are a sequence of concatenated 4-element MessagePack
// arrays, NOT a single top-level array. We stream-decode them one at a time.
func DecodeDataFrame(data []byte) ([]TopicData, error) {
	dec := msgpack.NewDecoder(bytes.NewReader(data))
	var entries []TopicData
	for {
		var arr []any
		err := dec.Decode(&arr)
		if err != nil {
			if err == io.EOF {
				return entries, nil
			}
			return nil, fmt.Errorf("msgpack unmarshal: %w", err)
		}

		if len(arr) < 4 {
			return nil, fmt.Errorf("entry %d: expected 4 elements, got %d", len(entries), len(arr))
		}

		topicID, err := toInt(arr[0])
		if err != nil {
			return nil, fmt.Errorf("entry %d topicID: %w", len(entries), err)
		}

		ts, err := toInt64(arr[1])
		if err != nil {
			return nil, fmt.Errorf("entry %d timestamp: %w", len(entries), err)
		}

		typeID, err := toInt(arr[2])
		if err != nil {
			return nil, fmt.Errorf("entry %d typeID: %w", len(entries), err)
		}

		entries = append(entries, TopicData{
			TopicID:         topicID,
			TimestampMicros: ts,
			TypeID:          typeID,
			Value:           arr[3],
		})
	}
}

// toInt wraps typeconv.ToInt with an error return.
func toInt(v any) (int, error) {
	n, ok := typeconv.ToInt(v)
	if !ok {
		return 0, fmt.Errorf("cannot convert %T to int", v)
	}
	return n, nil
}

// toInt64 wraps typeconv.ToInt64 with an error return.
func toInt64(v any) (int64, error) {
	n, ok := typeconv.ToInt64(v)
	if !ok {
		return 0, fmt.Errorf("cannot convert %T to int64", v)
	}
	return n, nil
}
