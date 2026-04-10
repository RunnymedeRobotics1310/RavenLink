---
status: pending
priority: p2
issue_id: 012
tags: [code-review, performance, uploader]
dependencies: []
---

# P2: Uploader creates http.Client per call + doesn't drain response body

## Problem Statement

`internal/uploader/uploader.go:406-412, 449` create a new `http.Client` on every HTTP call:
```go
client := &http.Client{Timeout: 30 * time.Second}
resp, err := client.Do(req)
...
resp.Body.Close()  // not read to EOF
```

Two issues:

1. **New client per call** — forces fresh TCP + TLS handshake on every request. For a typical upload (POST /session, GET /session, N × POST /data, POST /complete = 3-10 requests per file), this adds hundreds of ms of avoidable latency per upload.

2. **Unread response body** — Go's docs are explicit: *"If Body is not both read to EOF and closed, the Client's underlying RoundTripper ... may not be able to re-use a persistent TCP connection."* Even with a reused client, not draining defeats keep-alive.

Same pattern in `rbping/main.go`.

At high batch rates (500 entries × multiple batches per session × many sessions per day), this is meaningful waste.

## Findings

- **Location**: `internal/uploader/uploader.go:406-412, 449`
- **Agent**: Code quality reviewer flagged as P2 #12

## Proposed Solutions

### Fix: Single shared http.Client on the Uploader struct + drain body

```go
type Uploader struct {
    ...
    httpClient *http.Client
}

func New(...) *Uploader {
    return &Uploader{
        ...
        httpClient: &http.Client{Timeout: 30 * time.Second},
    }
}

// In postJSON/getJSON:
resp, err := u.httpClient.Do(req)
...
defer func() {
    io.Copy(io.Discard, resp.Body)
    resp.Body.Close()
}()
```

Effort: Small
Risk: None

## Recommended Action

Apply the fix. Simple, well-understood, measurable improvement.

## Technical Details

- Affected files:
  - `internal/uploader/uploader.go` (add httpClient field, use in post/getJSON, drain body)
  - Optionally `cmd/rbping/main.go` (same pattern)

## Acceptance Criteria

- [ ] `Uploader` has a single `httpClient` reused across calls
- [ ] Response bodies are drained before close in all success and error paths
- [ ] Benchmark: uploading a large session to a local HTTP server shows keep-alive connection reuse (check with `netstat` or pprof)
