// Package obsclient wraps the goobs OBS WebSocket v5 client with
// automatic reconnect logic, idempotent start/stop, and status queries.
package obsclient

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/andreykaipov/goobs"
	"github.com/andreykaipov/goobs/api/requests/record"
)

// Client wraps a goobs.Client with reconnect-and-retry semantics.
type Client struct {
	mu       sync.Mutex
	host     string
	port     int
	password string
	client   *goobs.Client
}

// New creates a new OBS client targeting the given host:port with the
// supplied password. It does not connect immediately; call Connect.
func New(host string, port int, password string) *Client {
	return &Client{
		host:     host,
		port:     port,
		password: password,
	}
}

// Connect establishes (or re-establishes) the WebSocket connection.
func (c *Client) Connect() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connectLocked()
}

func (c *Client) connectLocked() error {
	// Close any previous connection.
	if c.client != nil {
		_ = c.client.Disconnect()
		c.client = nil
	}

	addr := fmt.Sprintf("%s:%d", c.host, c.port)
	var opts []goobs.Option
	if c.password != "" {
		opts = append(opts, goobs.WithPassword(c.password))
	}

	cl, err := goobs.New(addr, opts...)
	if err != nil {
		slog.Warn("could not connect to OBS", "addr", addr, "err", err)
		return err
	}

	slog.Info("connected to OBS WebSocket", "addr", addr)
	c.client = cl
	return nil
}

// reconnectLocked attempts one reconnect while already holding the lock.
func (c *Client) reconnectLocked(method string) bool {
	slog.Info("attempting OBS reconnect before retrying", "method", method)
	return c.connectLocked() == nil
}

// Close tears down the WebSocket connection.
func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client != nil {
		_ = c.client.Disconnect()
		c.client = nil
	}
	slog.Info("OBS client closed")
}

// IsConnected returns true if the client can reach OBS.
func (c *Client) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client == nil {
		return false
	}
	_, err := c.client.General.GetVersion()
	if err != nil {
		c.client = nil
		return false
	}
	return true
}

// StartRecording asks OBS to begin recording. It retries once after a
// reconnect if the first attempt fails. "Already recording" is treated
// as success.
func (c *Client) StartRecording() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	for attempt := 0; attempt < 2; attempt++ {
		if c.client == nil {
			if attempt == 0 {
				c.reconnectLocked("StartRecording")
				continue
			}
			return false
		}

		_, err := c.client.Record.StartRecord(&record.StartRecordParams{})
		if err == nil {
			slog.Info(">>> OBS recording STARTED")
			return true
		}

		errMsg := strings.ToLower(err.Error())
		if strings.Contains(errMsg, "already") || strings.Contains(errMsg, "outputactive") {
			slog.Info("OBS was already recording")
			return true
		}

		slog.Warn("start_record failed", "attempt", attempt+1, "err", err)
		_ = c.client.Disconnect()
		c.client = nil
		if attempt == 0 {
			c.reconnectLocked("StartRecording")
		}
	}
	return false
}

// StopRecording asks OBS to stop recording. It retries once after a
// reconnect if the first attempt fails. "Not active" is treated as
// success.
func (c *Client) StopRecording() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	for attempt := 0; attempt < 2; attempt++ {
		if c.client == nil {
			if attempt == 0 {
				c.reconnectLocked("StopRecording")
				continue
			}
			return false
		}

		_, err := c.client.Record.StopRecord()
		if err == nil {
			slog.Info(">>> OBS recording STOPPED")
			return true
		}

		errMsg := strings.ToLower(err.Error())
		if strings.Contains(errMsg, "not active") || strings.Contains(errMsg, "outputnotactive") {
			slog.Info("OBS was not recording")
			return true
		}

		slog.Warn("stop_record failed", "attempt", attempt+1, "err", err)
		_ = c.client.Disconnect()
		c.client = nil
		if attempt == 0 {
			c.reconnectLocked("StopRecording")
		}
	}
	return false
}

// IsRecording returns true if OBS is currently recording.
func (c *Client) IsRecording() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client == nil {
		return false
	}
	resp, err := c.client.Record.GetRecordStatus()
	if err != nil {
		c.client = nil
		return false
	}
	return resp.OutputActive
}
