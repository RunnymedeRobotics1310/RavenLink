// Package ntclient implements a pure-Go NetworkTables 4 (NT4) client.
//
// It connects to a roboRIO over WebSocket, subscribes to topic prefixes,
// and streams TopicValue updates through a channel. The client handles
// reconnection with exponential backoff automatically.
package ntclient

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// NT4 WebSocket subprotocol identifiers.
const (
	subprotoV41 = "v4.1.networktables.first.wpi.edu"
	subprotoV4  = "networktables.first.wpi.edu"
)

// Default reconnection backoff parameters.
const (
	initialBackoff = 500 * time.Millisecond
	maxBackoff     = 10 * time.Second
	backoffFactor  = 2
)

// TopicValue represents a single value update received from NetworkTables.
type TopicValue struct {
	Name            string
	Type            string
	Value           any
	ServerTimeMicros int64
}

// topicInfo stores metadata for an announced topic.
type topicInfo struct {
	name   string
	typeID int
}

// Client is an NT4 WebSocket client that streams topic values.
type Client struct {
	url        string
	prefixes   []string
	clientName string

	values chan TopicValue
	cancel context.CancelFunc
	ctx    context.Context
	wg     sync.WaitGroup

	mu        sync.RWMutex
	connected bool
}

// New creates a new NT4 client. Call Connect to start the connection.
func New(clientName string, valueBuf int) *Client {
	if valueBuf <= 0 {
		valueBuf = 256
	}
	return &Client{
		clientName: clientName,
		values:     make(chan TopicValue, valueBuf),
	}
}

// Values returns a read-only channel that receives all topic value updates.
// The channel is closed when the client is closed via Close().
func (c *Client) Values() <-chan TopicValue {
	return c.values
}

// Connected reports whether the client currently has an active WebSocket
// connection to the server.
func (c *Client) Connected() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.connected
}

func (c *Client) setConnected(v bool) {
	c.mu.Lock()
	c.connected = v
	c.mu.Unlock()
}

// Connect starts the background goroutine that maintains the WebSocket
// connection to the NetworkTables server. It builds the URL from the given
// team number and port, subscribes to the given prefixes, and pushes all
// value changes to the Values() channel.
//
// The team number is used to derive the roboRIO IP address:
// 10.{team/100}.{team%100}.2
func (c *Client) Connect(team int, port int, prefixes []string) {
	ip := fmt.Sprintf("10.%d.%d.2", team/100, team%100)
	c.ConnectAddress(ip, port, prefixes)
}

// ConnectAddress starts the background connection loop using an explicit
// server address (IP or hostname).
func (c *Client) ConnectAddress(address string, port int, prefixes []string) {
	c.url = fmt.Sprintf("ws://%s:%d/nt/%s", address, port, c.clientName)
	c.prefixes = prefixes

	c.ctx, c.cancel = context.WithCancel(context.Background())
	c.wg.Add(1)
	go c.run()
}

// Subscribe updates the subscription prefixes. If the client is currently
// connected, the new subscription is sent immediately on the next
// reconnection. This is safe to call from any goroutine.
func (c *Client) Subscribe(prefixes []string) {
	c.mu.Lock()
	c.prefixes = prefixes
	c.mu.Unlock()
}

// Close cleanly shuts down the client, closing the WebSocket connection
// and the Values channel. It blocks until the background goroutine exits.
func (c *Client) Close() {
	if c.cancel != nil {
		c.cancel()
	}
	c.wg.Wait()
	close(c.values)
}

// run is the main background loop. It connects, reads frames, and
// reconnects with exponential backoff on failure.
func (c *Client) run() {
	defer c.wg.Done()

	backoff := initialBackoff

	for {
		wasConnected, err := c.session(c.ctx)
		c.setConnected(false)

		if c.ctx.Err() != nil {
			slog.Info("ntclient: shutting down")
			return
		}

		// If we were connected at some point, reset backoff so the next
		// reconnection attempt starts fast.
		if wasConnected {
			backoff = initialBackoff
		}

		slog.Warn("ntclient: connection lost", "err", err, "backoff", backoff)

		select {
		case <-c.ctx.Done():
			return
		case <-time.After(backoff):
		}

		backoff = min(backoff*backoffFactor, maxBackoff)
	}
}

// session runs a single WebSocket connection lifecycle: dial, subscribe,
// and read frames until an error or context cancellation. It returns true
// if the connection was successfully established (for backoff reset logic).
func (c *Client) session(ctx context.Context) (connected bool, err error) {
	slog.Info("ntclient: connecting", "url", c.url)

	conn, _, err := websocket.Dial(ctx, c.url, &websocket.DialOptions{
		Subprotocols: []string{subprotoV41, subprotoV4},
	})
	if err != nil {
		return false, fmt.Errorf("dial: %w", err)
	}
	defer conn.CloseNow()

	// Raise the read limit for large binary frames.
	conn.SetReadLimit(4 * 1024 * 1024) // 4 MiB

	c.setConnected(true)
	slog.Info("ntclient: connected", "url", c.url)

	// Send the initial subscription.
	c.mu.RLock()
	prefixes := c.prefixes
	c.mu.RUnlock()

	if err := c.sendSubscribe(ctx, conn, prefixes); err != nil {
		return true, fmt.Errorf("subscribe: %w", err)
	}

	// Topic ID -> topic metadata map, built from announce messages.
	topics := make(map[int]topicInfo)

	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			return true, fmt.Errorf("read: %w", err)
		}

		switch typ {
		case websocket.MessageText:
			if err := c.handleTextFrame(data, topics); err != nil {
				slog.Warn("ntclient: bad text frame", "err", err)
			}

		case websocket.MessageBinary:
			if err := c.handleBinaryFrame(data, topics); err != nil {
				slog.Warn("ntclient: bad binary frame", "err", err)
			}
		}
	}
}

// sendSubscribe sends a subscribe message for the given topic prefixes.
func (c *Client) sendSubscribe(ctx context.Context, conn *websocket.Conn, prefixes []string) error {
	msg := []SubscribeMessage{
		{
			Method: "subscribe",
			Params: SubscribeParams{
				Topics:  prefixes,
				SubUID:  1,
				Options: SubscribeOptions{All: true, Prefix: true},
			},
		},
	}

	payload, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal subscribe: %w", err)
	}

	slog.Debug("ntclient: subscribing", "prefixes", prefixes)
	return conn.Write(ctx, websocket.MessageText, payload)
}

// handleTextFrame parses a JSON text frame and processes announce /
// unannounce messages, updating the topic map accordingly.
func (c *Client) handleTextFrame(data []byte, topics map[int]topicInfo) error {
	// NT4 text frames are JSON arrays of message objects.
	var messages []json.RawMessage
	if err := json.Unmarshal(data, &messages); err != nil {
		return fmt.Errorf("unmarshal array: %w", err)
	}

	for _, raw := range messages {
		// Peek at the method to decide how to decode.
		var header struct {
			Method string `json:"method"`
		}
		if err := json.Unmarshal(raw, &header); err != nil {
			slog.Warn("ntclient: skip malformed message", "err", err)
			continue
		}

		switch header.Method {
		case "announce":
			var msg AnnounceMessage
			if err := json.Unmarshal(raw, &msg); err != nil {
				slog.Warn("ntclient: bad announce", "err", err)
				continue
			}
			typeID := typeNameToID(msg.Params.Type)
			topics[msg.Params.ID] = topicInfo{
				name:   msg.Params.Name,
				typeID: typeID,
			}
			slog.Debug("ntclient: topic announced",
				"id", msg.Params.ID,
				"name", msg.Params.Name,
				"type", msg.Params.Type,
			)

		case "unannounce":
			var msg UnannounceMessage
			if err := json.Unmarshal(raw, &msg); err != nil {
				slog.Warn("ntclient: bad unannounce", "err", err)
				continue
			}
			delete(topics, msg.Params.ID)
			slog.Debug("ntclient: topic unannounced",
				"id", msg.Params.ID,
				"name", msg.Params.Name,
			)

		default:
			slog.Debug("ntclient: ignoring message", "method", header.Method)
		}
	}
	return nil
}

// handleBinaryFrame decodes a MessagePack binary frame and sends resolved
// TopicValue entries to the values channel.
func (c *Client) handleBinaryFrame(data []byte, topics map[int]topicInfo) error {
	entries, err := DecodeDataFrame(data)
	if err != nil {
		return err
	}

	for _, e := range entries {
		info, ok := topics[e.TopicID]
		if !ok {
			slog.Debug("ntclient: unknown topic ID", "id", e.TopicID)
			continue
		}

		tv := TopicValue{
			Name:            info.name,
			Type:            TypeName(e.TypeID),
			Value:           e.Value,
			ServerTimeMicros: e.TimestampMicros,
		}

		select {
		case c.values <- tv:
		default:
			// Channel full — drop oldest to make room for the newest value.
			select {
			case <-c.values:
			default:
			}
			c.values <- tv
		}
	}
	return nil
}

// typeNameToID maps a type name string (from announce) to a type ID constant.
func typeNameToID(name string) int {
	switch name {
	case "boolean":
		return TypeBoolean
	case "double":
		return TypeDouble
	case "int":
		return TypeInt
	case "float":
		return TypeFloat
	case "string":
		return TypeString
	case "raw", "msgpack", "protobuf", "json":
		return TypeRaw
	case "boolean[]":
		return TypeBoolArray
	case "double[]":
		return TypeDoubleArray
	case "int[]":
		return TypeIntArray
	case "float[]":
		return TypeFloatArray
	case "string[]":
		return TypeStringArray
	default:
		return -1
	}
}
