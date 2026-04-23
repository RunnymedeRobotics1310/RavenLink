// Package obsclient wraps the goobs OBS WebSocket v5 client with
// automatic reconnect logic, idempotent start/stop, and status queries.
package obsclient

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/andreykaipov/goobs"
	"github.com/andreykaipov/goobs/api/requests/record"
)

// obsCallTimeout is the per-call timeout for synchronous goobs calls.
const obsCallTimeout = 3 * time.Second

// healthCheckInterval is how often the background health check pings OBS.
const healthCheckInterval = 5 * time.Second

// Client wraps a goobs.Client with reconnect-and-retry semantics.
//
// Connection state is exposed via a cached atomic boolean (see IsConnected)
// that is refreshed by a background health-check goroutine. This keeps the
// hot-path status queries cheap and non-blocking, and ensures that a hung
// OBS instance can never deadlock the main loop by blocking a lock holder
// on an RPC call.
type Client struct {
	mu        sync.Mutex
	host      string
	port      int
	password  string
	client    *goobs.Client
	connected atomic.Bool
	wg        sync.WaitGroup

	// probeInFlight guards against goroutine accumulation. When a
	// health-check probe times out, the goroutine running the goobs
	// RPC stays alive until Disconnect() propagates. If that doesn't
	// happen promptly (certain WebSocket failure modes), the next
	// health tick must not spawn a second goroutine — doing so every
	// 5 s for 24 h would leak ~17 k goroutines.
	probeInFlight atomic.Bool
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
	c.connected.Store(false)

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
	c.connected.Store(true)
	return nil
}

// Close tears down the WebSocket connection and waits for background
// goroutines (health check) to exit. The caller must cancel the context
// passed to StartHealthCheck before calling Close, otherwise Close will
// block waiting for the health loop to observe cancellation.
func (c *Client) Close() {
	c.wg.Wait()

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client != nil {
		_ = c.client.Disconnect()
		c.client = nil
	}
	c.connected.Store(false)
	slog.Info("OBS client closed")
}

// IsConnected returns the cached connection state. This is a cheap atomic
// load — the actual health probing happens in the background health check
// goroutine started via StartHealthCheck.
func (c *Client) IsConnected() bool {
	return c.connected.Load()
}

// StartHealthCheck launches a background goroutine that periodically pings
// OBS to refresh the cached connection state. On failure it attempts to
// reconnect. The goroutine exits when ctx is cancelled.
func (c *Client) StartHealthCheck(ctx context.Context) {
	c.wg.Add(1)
	go c.healthCheckLoop(ctx)
}

func (c *Client) healthCheckLoop(ctx context.Context) {
	defer c.wg.Done()
	ticker := time.NewTicker(healthCheckInterval)
	defer ticker.Stop()

	// Probe once immediately so IsConnected reflects reality before the
	// first tick fires.
	c.probeHealth()

	for {
		select {
		case <-ctx.Done():
			slog.Info("OBS health check loop exiting")
			return
		case <-ticker.C:
			c.probeHealth()
		}
	}
}

// probeHealth performs one health check iteration. It never holds the
// mutex across the network call, and uses callWithTimeout so a hung OBS
// cannot block the loop.
//
// The probeInFlight guard ensures that at most one goroutine is blocked
// inside a goobs RPC at any time. Without this, a hung OBS connection
// leaks one goroutine per health tick (~17 k/day).
func (c *Client) probeHealth() {
	c.mu.Lock()
	client := c.client
	c.mu.Unlock()

	if client == nil {
		// No live client — attempt a reconnect.
		c.mu.Lock()
		err := c.connectLocked()
		c.mu.Unlock()
		if err != nil {
			c.connected.Store(false)
		}
		return
	}

	// If a previous probe's goroutine is still stuck in a goobs RPC
	// (Disconnect didn't unblock it), skip this tick to prevent
	// unbounded goroutine accumulation.
	if !c.probeInFlight.CompareAndSwap(false, true) {
		slog.Debug("OBS health check still in flight, skipping")
		return
	}

	_, err := callWithTimeout(func() (any, error) {
		defer c.probeInFlight.Store(false)
		return client.General.GetVersion()
	}, obsCallTimeout)

	if err != nil {
		slog.Warn("OBS health check failed, will reconnect", "err", err)
		c.connected.Store(false)

		// Drop the dead client and try to reconnect. Only clear the
		// field if it's still the same pointer we probed — otherwise
		// another path (Connect, StartRecording) may have already
		// replaced it.
		c.mu.Lock()
		if c.client == client {
			_ = c.client.Disconnect()
			c.client = nil
		}
		_ = c.connectLocked()
		c.mu.Unlock()
		return
	}

	c.connected.Store(true)
}

// StartRecording asks OBS to begin recording. It retries once after a
// reconnect if the first attempt fails. "Already recording" is treated
// as success.
func (c *Client) StartRecording() bool {
	for attempt := 0; attempt < 2; attempt++ {
		c.mu.Lock()
		client := c.client
		c.mu.Unlock()

		if client == nil {
			if attempt == 0 {
				c.tryReconnect("StartRecording")
				continue
			}
			return false
		}

		_, err := callWithTimeout(func() (any, error) {
			return client.Record.StartRecord(&record.StartRecordParams{})
		}, obsCallTimeout)
		if err == nil {
			slog.Info(">>> OBS recording STARTED")
			c.connected.Store(true)
			return true
		}

		errMsg := strings.ToLower(err.Error())
		if strings.Contains(errMsg, "already") || strings.Contains(errMsg, "outputactive") {
			slog.Info("OBS was already recording")
			c.connected.Store(true)
			return true
		}

		slog.Warn("start_record failed", "attempt", attempt+1, "err", err)
		c.dropClient(client)
		if attempt == 0 {
			c.tryReconnect("StartRecording")
		}
	}
	return false
}

// StopRecording asks OBS to stop recording. It retries once after a
// reconnect if the first attempt fails. "Not active" is treated as
// success.
func (c *Client) StopRecording() bool {
	for attempt := 0; attempt < 2; attempt++ {
		c.mu.Lock()
		client := c.client
		c.mu.Unlock()

		if client == nil {
			if attempt == 0 {
				c.tryReconnect("StopRecording")
				continue
			}
			return false
		}

		_, err := callWithTimeout(func() (any, error) {
			return client.Record.StopRecord()
		}, obsCallTimeout)
		if err == nil {
			slog.Info(">>> OBS recording STOPPED")
			c.connected.Store(true)
			return true
		}

		errMsg := strings.ToLower(err.Error())
		if strings.Contains(errMsg, "not active") || strings.Contains(errMsg, "outputnotactive") {
			slog.Info("OBS was not recording")
			c.connected.Store(true)
			return true
		}

		slog.Warn("stop_record failed", "attempt", attempt+1, "err", err)
		c.dropClient(client)
		if attempt == 0 {
			c.tryReconnect("StopRecording")
		}
	}
	return false
}

// IsRecording returns true if OBS is currently recording. It is not
// called on the hot path, but still uses callWithTimeout so a hung OBS
// cannot block the caller.
func (c *Client) IsRecording() bool {
	c.mu.Lock()
	client := c.client
	c.mu.Unlock()
	if client == nil {
		return false
	}
	resp, err := callWithTimeout(func() (*record.GetRecordStatusResponse, error) {
		return client.Record.GetRecordStatus()
	}, obsCallTimeout)
	if err != nil {
		c.dropClient(client)
		return false
	}
	return resp.OutputActive
}

// dropClient clears c.client if it still matches the given pointer, and
// marks the connection as down. Used after a failed RPC call.
func (c *Client) dropClient(expected *goobs.Client) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client == expected && c.client != nil {
		_ = c.client.Disconnect()
		c.client = nil
	}
	c.connected.Store(false)
}

// tryReconnect attempts a single reconnect. It logs the intent and swallows
// the error (caller will retry the underlying RPC on the next attempt).
func (c *Client) tryReconnect(method string) {
	slog.Info("attempting OBS reconnect before retrying", "method", method)
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = c.connectLocked()
}

// callWithTimeout runs fn in a goroutine and returns its result, or an
// error if it doesn't complete within timeout. If the timeout fires the
// goroutine is leaked until fn returns (bounded by connection close) or
// the process exits.
func callWithTimeout[T any](fn func() (T, error), timeout time.Duration) (T, error) {
	type result struct {
		v   T
		err error
	}
	ch := make(chan result, 1)
	go func() {
		v, err := fn()
		ch <- result{v: v, err: err}
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case r := <-ch:
		return r.v, r.err
	case <-timer.C:
		var zero T
		return zero, errors.New("obs call timed out")
	}
}
