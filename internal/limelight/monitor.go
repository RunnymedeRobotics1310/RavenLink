// Package limelight polls Limelight cameras' /results HTTP endpoint on
// a fixed interval and streams uptime and reachability updates as
// ntclient.TopicValue messages. The output channel feeds the existing
// JSONL logger, so Limelight data rides the same session-lifecycle,
// replay, upload, and WPILog-export pipeline as NetworkTables values.
//
// The topic names emitted are:
//
//	/RavenLink/Limelight/<last_octet>/uptime_ms  (int)    - Limelight ts
//	/RavenLink/Limelight/<last_octet>/reachable  (boolean)
//
// On a failed poll (timeout, HTTP error, non-2xx, malformed JSON,
// missing ts field) only the reachable=false update is emitted.
package limelight

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/RunnymedeRobotics1310/RavenLink/internal/ntclient"
)

const (
	limelightPort = 5807
	resultsPath   = "/results"
)

// Monitor polls one or more Limelights on a fixed cadence.
type Monitor struct {
	urlFor       func(octet int) string
	octets       []int
	pollInterval time.Duration
	timeout      time.Duration
	httpClient   *http.Client
	values       chan ntclient.TopicValue
	pollWG       sync.WaitGroup // tracks in-flight poll goroutines

	// State for transition-based logging. Multiple poll goroutines run
	// concurrently (one per octet per tick), so map access is guarded.
	stateMu sync.Mutex
	states  map[int]octetState
}

// octetState tracks the last known reachability of a single camera so
// the monitor can log transitions (reachable↔unreachable) rather than
// every individual poll result — silencing logs during sustained
// outages while still surfacing actionable state changes.
type octetState struct {
	known     bool // false until we've observed at least one poll
	reachable bool
}

// New creates a Monitor that polls each Limelight at
// 10.TE.AM.<octet>:5807/results every pollInterval, with each request
// bounded by timeout. bufSize is the output channel capacity; 0 means
// use the default (32).
func New(team int, lastOctets []int, pollInterval, timeout time.Duration, bufSize int) *Monitor {
	return newMonitor(
		func(octet int) string {
			return fmt.Sprintf("http://10.%d.%d.%d:%d%s",
				team/100, team%100, octet, limelightPort, resultsPath)
		},
		lastOctets, pollInterval, timeout, bufSize,
	)
}

// newMonitor is the testable constructor: the URL builder is injected
// so tests can point every octet at a single httptest.Server.
func newMonitor(urlFor func(int) string, octets []int, pollInterval, timeout time.Duration, bufSize int) *Monitor {
	if bufSize <= 0 {
		bufSize = 32
	}
	return &Monitor{
		urlFor:       urlFor,
		octets:       octets,
		pollInterval: pollInterval,
		timeout:      timeout,
		httpClient:   &http.Client{},
		values:       make(chan ntclient.TopicValue, bufSize),
		states:       make(map[int]octetState, len(octets)),
	}
}

// Values returns the channel of TopicValue updates produced by the
// monitor. It closes when Run returns.
func (m *Monitor) Values() <-chan ntclient.TopicValue {
	return m.values
}

// Run is the actor loop. It fires an immediate first poll, then polls
// every pollInterval until ctx is cancelled. Closes the output channel
// on exit.
func (m *Monitor) Run(ctx context.Context) {
	// Defer order is LIFO: wait for in-flight poll goroutines to
	// finish before closing the output channel, so a late poll can't
	// send on a closed channel and panic. The per-request context
	// derives from ctx, so cancelling ctx aborts in-flight HTTP
	// requests within their timeout window — the wait is bounded.
	defer close(m.values)
	defer m.pollWG.Wait()

	if len(m.octets) == 0 {
		slog.Info("limelight: no octets configured, monitor idle")
		<-ctx.Done()
		return
	}

	slog.Info("limelight: monitor started",
		"octets", m.octets,
		"poll_interval", m.pollInterval,
		"timeout", m.timeout,
	)

	// Fire an immediate first poll so we don't wait pollInterval for
	// the first data point.
	m.tick(ctx)

	ticker := time.NewTicker(m.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.tick(ctx)
		}
	}
}

// tick fires one poll goroutine per configured octet. The goroutines
// are fire-and-forget; each is bounded by a per-request context
// deadline so a slow camera can't delay polls of a fast one.
func (m *Monitor) tick(ctx context.Context) {
	for _, octet := range m.octets {
		m.pollWG.Add(1)
		go func(o int) {
			defer m.pollWG.Done()
			m.poll(ctx, o)
		}(octet)
	}
}

// limelightResponse is the minimal shape we care about from the
// /results JSON body. A pointer distinguishes "field absent" from
// "field present with value 0" (a freshly-booted Limelight is at
// ts=0 and is a legitimate datapoint, not an error).
type limelightResponse struct {
	TS *int64 `json:"ts"`
}

// poll issues one request to octet's Limelight. On success it sends
// both uptime_ms and reachable=true; on any failure it sends only
// reachable=false. State transitions (reachable↔unreachable) and the
// first poll are logged; ongoing failures are silent.
func (m *Monitor) poll(ctx context.Context, octet int) {
	reqCtx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()

	url := m.urlFor(octet)

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		m.recordResult(octet, url, false, fmt.Sprintf("build request: %v", err))
		return
	}

	resp, err := m.httpClient.Do(req)
	if err != nil {
		m.recordResult(octet, url, false, err.Error())
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		m.recordResult(octet, url, false, fmt.Sprintf("http %d", resp.StatusCode))
		return
	}

	var body limelightResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		m.recordResult(octet, url, false, fmt.Sprintf("decode json: %v", err))
		return
	}
	if body.TS == nil {
		m.recordResult(octet, url, false, "response missing ts field")
		return
	}

	m.send(uptimeTopic(octet), "int", *body.TS)
	m.recordResult(octet, url, true, "")
}

// recordResult updates the per-octet reachability state, logs
// transitions (and the first poll), and emits the reachable TopicValue.
// Only transitions are logged — sustained outages produce a single WARN
// on the false transition, not one per failed poll. A string reason is
// accepted rather than an error so non-error failures (non-2xx, missing
// field) share the code path cleanly.
func (m *Monitor) recordResult(octet int, url string, ok bool, reason string) {
	m.stateMu.Lock()
	prev := m.states[octet]
	changed := !prev.known || prev.reachable != ok
	m.states[octet] = octetState{known: true, reachable: ok}
	m.stateMu.Unlock()

	if changed {
		switch {
		case !prev.known && ok:
			slog.Info("limelight: poll succeeded", "octet", octet, "url", url)
		case !prev.known && !ok:
			slog.Warn("limelight: poll failed", "octet", octet, "url", url, "reason", reason)
		case prev.reachable && !ok:
			slog.Warn("limelight: camera went unreachable", "octet", octet, "url", url, "reason", reason)
		case !prev.reachable && ok:
			slog.Info("limelight: camera back online", "octet", octet, "url", url)
		}
	}

	m.sendReachable(octet, ok)
}

func (m *Monitor) sendReachable(octet int, reachable bool) {
	m.send(reachableTopic(octet), "boolean", reachable)
}

// send does a non-blocking send into the output channel. If the
// channel is full (consumer is stalled), drop the update rather than
// block the monitor. 1 Hz × N cameras is low enough that a 32-slot
// buffer should never fill unless something downstream is wedged, in
// which case adding pressure here wouldn't help.
func (m *Monitor) send(name, typeName string, value any) {
	tv := ntclient.TopicValue{Name: name, Type: typeName, Value: value}
	select {
	case m.values <- tv:
	default:
		slog.Debug("limelight: output channel full, dropping update", "name", name)
	}
}

func uptimeTopic(octet int) string {
	return fmt.Sprintf("/RavenLink/Limelight/%d/uptime_ms", octet)
}

func reachableTopic(octet int) string {
	return fmt.Sprintf("/RavenLink/Limelight/%d/reachable", octet)
}
