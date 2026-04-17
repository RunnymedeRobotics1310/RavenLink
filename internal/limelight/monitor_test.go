package limelight

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RunnymedeRobotics1310/RavenLink/internal/ntclient"
)

// collect reads values from ch until it sees n of them OR the deadline
// passes. Returns whatever it accumulated. It's the tests' job to
// assert on the result.
func collect(t *testing.T, ch <-chan ntclient.TopicValue, n int, deadline time.Duration) []ntclient.TopicValue {
	t.Helper()
	got := make([]ntclient.TopicValue, 0, n)
	timeout := time.After(deadline)
	for len(got) < n {
		select {
		case v, ok := <-ch:
			if !ok {
				return got
			}
			got = append(got, v)
		case <-timeout:
			return got
		}
	}
	return got
}

// findByName returns the first value matching name, or a zero value
// and ok=false. Useful because poll order across goroutines isn't
// deterministic.
func findByName(vs []ntclient.TopicValue, name string) (ntclient.TopicValue, bool) {
	for _, v := range vs {
		if v.Name == name {
			return v, true
		}
	}
	return ntclient.TopicValue{}, false
}

// newTestMonitor builds a Monitor whose every octet URL points at the
// provided httptest.Server. One-tick pollInterval is 50 ms so tests
// finish fast; timeout defaults to 500 ms unless overridden.
func newTestMonitor(t *testing.T, serverURL string, octets []int, pollInterval, timeout time.Duration) *Monitor {
	t.Helper()
	return newMonitor(
		func(octet int) string { return serverURL + resultsPath },
		octets,
		pollInterval,
		timeout,
		32,
	)
}

// TestMonitor_HappyPath — the happy path. A server returning
// {"ts":12345} should produce exactly one uptime_ms int update with
// value 12345 and one reachable bool update with value true, both
// keyed to the configured octet.
func TestMonitor_HappyPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ts":12345}`))
	}))
	defer server.Close()

	m := newTestMonitor(t, server.URL, []int{11}, 10*time.Second, 500*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)

	got := collect(t, m.Values(), 2, 2*time.Second)
	if len(got) < 2 {
		t.Fatalf("expected 2 values, got %d: %+v", len(got), got)
	}

	uptime, ok := findByName(got, "/RavenLink/Limelight/11/uptime_ms")
	if !ok {
		t.Fatalf("missing uptime_ms in %+v", got)
	}
	if uptime.Type != "int" {
		t.Errorf("uptime type: got %q, want %q", uptime.Type, "int")
	}
	if v, ok := uptime.Value.(int64); !ok || v != 12345 {
		t.Errorf("uptime value: got %v (%T), want int64(12345)", uptime.Value, uptime.Value)
	}

	reachable, ok := findByName(got, "/RavenLink/Limelight/11/reachable")
	if !ok {
		t.Fatalf("missing reachable in %+v", got)
	}
	if reachable.Type != "boolean" {
		t.Errorf("reachable type: got %q, want %q", reachable.Type, "boolean")
	}
	if v, ok := reachable.Value.(bool); !ok || !v {
		t.Errorf("reachable value: got %v, want true", reachable.Value)
	}
}

// TestMonitor_MultipleOctets — two configured octets should each
// produce their own pair of topics. Keys include the octet so they
// don't collide.
func TestMonitor_MultipleOctets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ts":7}`))
	}))
	defer server.Close()

	m := newTestMonitor(t, server.URL, []int{11, 12}, 10*time.Second, 500*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)

	got := collect(t, m.Values(), 4, 2*time.Second)
	if len(got) < 4 {
		t.Fatalf("expected 4 values (2 octets × 2 topics), got %d: %+v", len(got), got)
	}

	want := []string{
		"/RavenLink/Limelight/11/uptime_ms",
		"/RavenLink/Limelight/11/reachable",
		"/RavenLink/Limelight/12/uptime_ms",
		"/RavenLink/Limelight/12/reachable",
	}
	for _, name := range want {
		if _, ok := findByName(got, name); !ok {
			t.Errorf("missing %s in %+v", name, got)
		}
	}
}

// TestMonitor_ZeroTS — a freshly-booted Limelight reports ts=0. That
// is a legitimate value, not a sentinel — must emit uptime=0 plus
// reachable=true, not reachable=false.
func TestMonitor_ZeroTS(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ts":0}`))
	}))
	defer server.Close()

	m := newTestMonitor(t, server.URL, []int{11}, 10*time.Second, 500*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)

	got := collect(t, m.Values(), 2, 2*time.Second)
	if len(got) < 2 {
		t.Fatalf("expected 2 values, got %d: %+v", len(got), got)
	}

	uptime, _ := findByName(got, "/RavenLink/Limelight/11/uptime_ms")
	if v, ok := uptime.Value.(int64); !ok || v != 0 {
		t.Errorf("uptime value: got %v, want 0", uptime.Value)
	}
	reachable, _ := findByName(got, "/RavenLink/Limelight/11/reachable")
	if v, ok := reachable.Value.(bool); !ok || !v {
		t.Errorf("reachable: got %v, want true", reachable.Value)
	}
}

// TestMonitor_Timeout — server delays beyond the per-request timeout.
// Expect a single reachable=false update and no uptime_ms update.
func TestMonitor_Timeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte(`{"ts":999}`))
	}))
	defer server.Close()

	m := newTestMonitor(t, server.URL, []int{11}, 10*time.Second, 50*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)

	got := collect(t, m.Values(), 1, 1*time.Second)
	if len(got) != 1 {
		t.Fatalf("expected 1 value (reachable=false only), got %d: %+v", len(got), got)
	}
	if got[0].Name != "/RavenLink/Limelight/11/reachable" {
		t.Errorf("expected reachable update, got %+v", got[0])
	}
	if v, ok := got[0].Value.(bool); !ok || v {
		t.Errorf("reachable value: got %v, want false", got[0].Value)
	}
}

// TestMonitor_Non2xx — HTTP 500 → reachable=false only.
func TestMonitor_Non2xx(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	m := newTestMonitor(t, server.URL, []int{11}, 10*time.Second, 500*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)

	got := collect(t, m.Values(), 1, 1*time.Second)
	if len(got) != 1 {
		t.Fatalf("expected 1 value, got %d: %+v", len(got), got)
	}
	if got[0].Name != "/RavenLink/Limelight/11/reachable" {
		t.Errorf("got %+v", got[0])
	}
	if v, _ := got[0].Value.(bool); v {
		t.Errorf("want reachable=false, got true")
	}
}

// TestMonitor_NonJSONBody — HTTP 200 with an HTML body → reachable=false.
func TestMonitor_NonJSONBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><body>not json</body></html>`))
	}))
	defer server.Close()

	m := newTestMonitor(t, server.URL, []int{11}, 10*time.Second, 500*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)

	got := collect(t, m.Values(), 1, 1*time.Second)
	if len(got) != 1 || got[0].Name != "/RavenLink/Limelight/11/reachable" {
		t.Fatalf("expected reachable=false only, got %+v", got)
	}
	if v, _ := got[0].Value.(bool); v {
		t.Errorf("want reachable=false, got true")
	}
}

// TestMonitor_MissingTSField — 200 + valid JSON but no `ts` field →
// reachable=false. The monitor can't do its job without a ts value.
func TestMonitor_MissingTSField(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"pipeline":0,"tx":1.2}`))
	}))
	defer server.Close()

	m := newTestMonitor(t, server.URL, []int{11}, 10*time.Second, 500*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)

	got := collect(t, m.Values(), 1, 1*time.Second)
	if len(got) != 1 || got[0].Name != "/RavenLink/Limelight/11/reachable" {
		t.Fatalf("expected reachable=false only, got %+v", got)
	}
	if v, _ := got[0].Value.(bool); v {
		t.Errorf("want reachable=false, got true")
	}
}

// TestMonitor_ConnectionRefused — target host that doesn't exist.
// Dial fails fast within the request timeout; expect reachable=false.
func TestMonitor_ConnectionRefused(t *testing.T) {
	m := newMonitor(
		// A reserved TEST-NET-1 address that won't respond; combined
		// with a short timeout this fails quickly.
		func(octet int) string { return "http://192.0.2.1:9999/results" },
		[]int{11},
		10*time.Second,
		100*time.Millisecond,
		32,
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)

	got := collect(t, m.Values(), 1, 500*time.Millisecond)
	if len(got) != 1 || got[0].Name != "/RavenLink/Limelight/11/reachable" {
		t.Fatalf("expected reachable=false, got %+v", got)
	}
	if v, _ := got[0].Value.(bool); v {
		t.Errorf("want reachable=false, got true")
	}
}

// TestMonitor_ContextCancel — cancelling the Run context closes the
// output channel and Run returns promptly.
func TestMonitor_ContextCancel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ts":1}`))
	}))
	defer server.Close()

	m := newTestMonitor(t, server.URL, []int{11}, 10*time.Second, 500*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		m.Run(ctx)
		close(done)
	}()

	// Let it get started.
	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("Run did not return within 1s of context cancel")
	}

	// Values channel must be closed. Drain any residual values.
	drainDeadline := time.After(500 * time.Millisecond)
	for {
		select {
		case _, ok := <-m.Values():
			if !ok {
				return // closed cleanly
			}
		case <-drainDeadline:
			t.Fatal("Values channel still open after cancel")
		}
	}
}

// TestMonitor_RepeatedTicks — over multiple intervals we should see
// many updates, with reboot-like behavior visible when ts decreases.
func TestMonitor_RepeatedTicks(t *testing.T) {
	var counter atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := counter.Add(1)
		// Third response fakes a reboot: ts goes backward.
		ts := n * 100
		if n == 3 {
			ts = 1
		}
		_, _ = w.Write([]byte(fmt.Sprintf(`{"ts":%d}`, ts)))
	}))
	defer server.Close()

	m := newTestMonitor(t, server.URL, []int{11}, 30*time.Millisecond, 500*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)

	// Collect ~3 ticks worth of updates (6 values: uptime+reachable × 3).
	got := collect(t, m.Values(), 6, 1*time.Second)
	if len(got) < 6 {
		t.Fatalf("expected at least 6 values over 3 ticks, got %d: %+v", len(got), got)
	}

	// Pull out uptime values in arrival order to verify the
	// simulated-reboot pattern is visible.
	var uptimes []int64
	for _, v := range got {
		if v.Name == "/RavenLink/Limelight/11/uptime_ms" {
			uptimes = append(uptimes, v.Value.(int64))
		}
	}
	if len(uptimes) < 3 {
		t.Fatalf("expected >= 3 uptime samples, got %d", len(uptimes))
	}
	// Third sample (reboot) must be less than second sample.
	if uptimes[2] >= uptimes[1] {
		t.Errorf("expected reboot at sample 3 (ts decrease): got %v", uptimes)
	}
}

// TestMonitor_EmptyOctets — no octets configured → no HTTP calls,
// monitor sits idle until ctx is cancelled.
func TestMonitor_EmptyOctets(t *testing.T) {
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"ts":1}`))
	}))
	defer server.Close()

	m := newTestMonitor(t, server.URL, []int{}, 10*time.Millisecond, 100*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()

	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done

	if h := hits.Load(); h != 0 {
		t.Errorf("expected 0 server hits with no octets, got %d", h)
	}
}

// TestMonitor_URLConstruction — the production-path URL builder
// derives 10.TE.AM.<octet>:5807/results correctly from a team number.
// Pins the URL shape so a refactor to RobotIP() helpers doesn't
// silently break.
func TestMonitor_URLConstruction(t *testing.T) {
	m := New(1310, []int{11, 12}, time.Second, 100*time.Millisecond, 32)

	want := map[int]string{
		11: "http://10.13.10.11:5807/results",
		12: "http://10.13.10.12:5807/results",
	}
	for octet, expected := range want {
		got := m.urlFor(octet)
		if got != expected {
			t.Errorf("urlFor(%d): got %q, want %q", octet, got, expected)
		}
	}
}

// TestMonitor_ConcurrentTicksDoNotDeadlock — with an aggressive
// pollInterval and a slow consumer, the monitor's non-blocking send
// prevents deadlock. The output channel will drop updates rather
// than wedge.
func TestMonitor_ConcurrentTicksDoNotDeadlock(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ts":1}`))
	}))
	defer server.Close()

	m := newTestMonitor(t, server.URL, []int{11, 12}, 5*time.Millisecond, 100*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()

	// Don't drain at all for 200 ms. Channel buffer fills, updates
	// get dropped. Nothing should deadlock.
	time.Sleep(200 * time.Millisecond)
	cancel()

	// Drain remaining.
	var mu sync.Mutex
	var seen []string
	drainDone := make(chan struct{})
	go func() {
		for v := range m.Values() {
			mu.Lock()
			seen = append(seen, v.Name)
			mu.Unlock()
		}
		close(drainDone)
	}()

	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("Run did not return after cancel (deadlocked?)")
	}
	<-drainDone

	// Sanity: whatever we did see must have a sensible shape.
	for _, name := range seen {
		if !strings.HasPrefix(name, "/RavenLink/Limelight/") {
			t.Errorf("unexpected topic name: %q", name)
		}
	}
}
