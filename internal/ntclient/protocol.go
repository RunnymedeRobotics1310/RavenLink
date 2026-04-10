package ntclient

import (
	"fmt"

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
func EncodeDataFrame(entries []TopicData) ([]byte, error) {
	raw := make([]any, len(entries))
	for i, e := range entries {
		raw[i] = []any{e.TopicID, e.TimestampMicros, e.TypeID, e.Value}
	}
	return msgpack.Marshal(raw)
}

// DecodeDataFrame decodes a MessagePack binary payload into a slice of
// TopicData entries. The payload must be an array of 4-element arrays.
func DecodeDataFrame(data []byte) ([]TopicData, error) {
	var raw [][]any
	if err := msgpack.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("msgpack unmarshal: %w", err)
	}

	entries := make([]TopicData, 0, len(raw))
	for i, arr := range raw {
		if len(arr) < 4 {
			return nil, fmt.Errorf("entry %d: expected 4 elements, got %d", i, len(arr))
		}

		topicID, err := toInt(arr[0])
		if err != nil {
			return nil, fmt.Errorf("entry %d topicID: %w", i, err)
		}

		ts, err := toInt64(arr[1])
		if err != nil {
			return nil, fmt.Errorf("entry %d timestamp: %w", i, err)
		}

		typeID, err := toInt(arr[2])
		if err != nil {
			return nil, fmt.Errorf("entry %d typeID: %w", i, err)
		}

		entries = append(entries, TopicData{
			TopicID:         topicID,
			TimestampMicros: ts,
			TypeID:          typeID,
			Value:           arr[3],
		})
	}
	return entries, nil
}

// toInt converts a msgpack-decoded numeric value to int.
func toInt(v any) (int, error) {
	switch n := v.(type) {
	case int8:
		return int(n), nil
	case int16:
		return int(n), nil
	case int32:
		return int(n), nil
	case int64:
		return int(n), nil
	case uint8:
		return int(n), nil
	case uint16:
		return int(n), nil
	case uint32:
		return int(n), nil
	case uint64:
		return int(n), nil
	case float32:
		return int(n), nil
	case float64:
		return int(n), nil
	default:
		return 0, fmt.Errorf("cannot convert %T to int", v)
	}
}

// toInt64 converts a msgpack-decoded numeric value to int64.
func toInt64(v any) (int64, error) {
	switch n := v.(type) {
	case int8:
		return int64(n), nil
	case int16:
		return int64(n), nil
	case int32:
		return int64(n), nil
	case int64:
		return n, nil
	case uint8:
		return int64(n), nil
	case uint16:
		return int64(n), nil
	case uint32:
		return int64(n), nil
	case uint64:
		return int64(n), nil
	case float32:
		return int64(n), nil
	case float64:
		return int64(n), nil
	default:
		return 0, fmt.Errorf("cannot convert %T to int64", v)
	}
}
