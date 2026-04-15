package wpilog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

// Convert transforms JSONL session data (as produced by ntlogger) into
// a WPILog binary file. The conversion is two-pass:
//
//  1. Parse all lines, collect unique (key, type) pairs, assign WPILog
//     entry IDs, and compute the base timestamp.
//  2. Write the file header, Start control records, and data records.
//
// Match markers (match_start, match_end) are synthesized as string
// entries on the virtual topic /RavenLink/MatchEvent.
func Convert(jsonlData []byte, team int, sessionID string) ([]byte, error) {
	lines := splitLines(jsonlData)

	// ---- First pass: collect topics and compute base timestamp ----

	type topicKey struct {
		name    string
		nt4Type string
	}

	topicOrder := []topicKey{}            // preserves first-seen order
	topicSet := map[topicKey]struct{}{}   // dedup
	var dataLines []map[string]any        // data entries
	var markerLines []map[string]any      // match_start, match_end
	var sessionStartTS float64            // wall-clock ts from session_start

	var minServerTS int64 = math.MaxInt64
	hasServerTS := false

	for _, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal(line, &entry); err != nil {
			continue // skip malformed lines
		}

		entryType, _ := entry["type"].(string)

		switch {
		case entryType == "session_start":
			if ts, ok := entry["ts"].(float64); ok {
				sessionStartTS = ts
			}

		case entryType == "session_end":
			// Skip — no data to export.

		case entryType == "match_start" || entryType == "match_end":
			markerLines = append(markerLines, entry)

		default:
			// Data entry: type field is the NT4 type name, key is present.
			if _, hasKey := entry["key"]; !hasKey {
				continue
			}
			dataLines = append(dataLines, entry)

			key, _ := entry["key"].(string)
			tk := topicKey{name: key, nt4Type: entryType}
			if _, exists := topicSet[tk]; !exists {
				topicSet[tk] = struct{}{}
				topicOrder = append(topicOrder, tk)
			}

			if sts, ok := serverTS(entry); ok && sts > 0 {
				if sts < minServerTS {
					minServerTS = sts
				}
				hasServerTS = true
			}
		}
	}

	if !hasServerTS {
		minServerTS = 0
	}

	// Assign entry IDs. Topics get IDs starting at 1.
	entryIDs := map[topicKey]uint32{}
	nextID := uint32(1)
	for _, tk := range topicOrder {
		entryIDs[tk] = nextID
		nextID++
	}

	// Reserve an entry ID for the synthetic match event topic if we
	// have any markers.
	var matchEventID uint32
	if len(markerLines) > 0 {
		matchEventID = nextID
		nextID++
	}

	// ---- Second pass: write WPILog ----

	var buf bytes.Buffer

	extraHeader := fmt.Sprintf(`{"team":%d,"source":"RavenLink","session":"%s"}`, team, sessionID)
	if err := WriteHeader(&buf, extraHeader); err != nil {
		return nil, fmt.Errorf("write header: %w", err)
	}

	// Write Start records for each topic (timestamp 0 for control records).
	for _, tk := range topicOrder {
		id := entryIDs[tk]
		wpiType := MapNT4Type(tk.nt4Type)
		if err := WriteStartRecord(&buf, id, tk.name, wpiType, "", 0); err != nil {
			return nil, fmt.Errorf("start record %q: %w", tk.name, err)
		}
	}

	// Start record for synthetic match event topic.
	if matchEventID > 0 {
		if err := WriteStartRecord(&buf, matchEventID, "/RavenLink/MatchEvent", "string", "", 0); err != nil {
			return nil, fmt.Errorf("start record /RavenLink/MatchEvent: %w", err)
		}
	}

	// Write data records.
	for _, entry := range dataLines {
		key, _ := entry["key"].(string)
		nt4Type, _ := entry["type"].(string)
		tk := topicKey{name: key, nt4Type: nt4Type}
		id, ok := entryIDs[tk]
		if !ok {
			continue
		}

		ts := resolveTimestamp(entry, sessionStartTS, minServerTS)

		payload, err := EncodeValue(nt4Type, entry["value"])
		if err != nil {
			continue // skip entries that can't be encoded
		}

		if err := WriteDataRecord(&buf, id, ts, payload); err != nil {
			return nil, fmt.Errorf("data record: %w", err)
		}
	}

	// Write synthetic match event records.
	if matchEventID > 0 {
		// Sort markers by wall-clock ts for consistent ordering.
		sort.Slice(markerLines, func(i, j int) bool {
			ti, _ := markerLines[i]["ts"].(float64)
			tj, _ := markerLines[j]["ts"].(float64)
			return ti < tj
		})
		for _, entry := range markerLines {
			eventType, _ := entry["type"].(string)
			ts := resolveMarkerTimestamp(entry, sessionStartTS, minServerTS)
			payload := []byte(eventType)
			if err := WriteDataRecord(&buf, matchEventID, ts, payload); err != nil {
				return nil, fmt.Errorf("marker record: %w", err)
			}
		}
	}

	return buf.Bytes(), nil
}

// resolveTimestamp returns the WPILog timestamp (microseconds, relative
// to base) for a data entry. Prefers server_ts; falls back to wall-clock
// when server_ts is 0 or absent.
func resolveTimestamp(entry map[string]any, sessionStartTS float64, baseTS int64) uint64 {
	if sts, ok := serverTS(entry); ok && sts > 0 {
		rel := sts - baseTS
		if rel < 0 {
			return 0
		}
		return uint64(rel)
	}
	// Fallback: synthesize from wall-clock ts.
	return wallClockToMicros(entry, sessionStartTS)
}

// resolveMarkerTimestamp computes a WPILog timestamp for a match marker.
// Markers never have server_ts, so we always use wall-clock.
func resolveMarkerTimestamp(entry map[string]any, sessionStartTS float64, baseTS int64) uint64 {
	return wallClockToMicros(entry, sessionStartTS)
}

// wallClockToMicros converts a wall-clock ts (Unix seconds) to
// microseconds relative to session start.
func wallClockToMicros(entry map[string]any, sessionStartTS float64) uint64 {
	ts, ok := entry["ts"].(float64)
	if !ok {
		return 0
	}
	delta := ts - sessionStartTS
	if delta < 0 {
		return 0
	}
	return uint64(delta * 1_000_000)
}

// serverTS extracts the server_ts field as int64 microseconds.
func serverTS(entry map[string]any) (int64, bool) {
	v, ok := entry["server_ts"]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case int64:
		return n, true
	default:
		return 0, false
	}
}

// splitLines splits data on newlines, returning non-empty byte slices.
func splitLines(data []byte) [][]byte {
	raw := bytes.Split(data, []byte("\n"))
	lines := make([][]byte, 0, len(raw))
	for _, l := range raw {
		l = bytes.TrimSpace(l)
		if len(l) > 0 {
			lines = append(lines, l)
		}
	}
	return lines
}

// ExtractSessionID parses the session_id from a JSONL filename like
// "20260411T151030Z_0284a1cf.jsonl" → "0284a1cf".
func ExtractSessionID(filename string) string {
	name := strings.TrimSuffix(filename, ".jsonl")
	if idx := strings.LastIndex(name, "_"); idx >= 0 && idx+1 < len(name) {
		return name[idx+1:]
	}
	return ""
}

// ExtractDate parses the UTC date from a JSONL filename like
// "20260411T151030Z_0284a1cf.jsonl" → "2026-04-11T15:10:30Z".
func ExtractDate(filename string) string {
	// Format: 20060102T150405Z_...
	if len(filename) < 16 {
		return ""
	}
	ts := filename[:16] // "20260411T151030Z"
	if len(ts) != 16 || ts[8] != 'T' || ts[15] != 'Z' {
		return ""
	}
	return fmt.Sprintf("%s-%s-%sT%s:%s:%sZ",
		ts[0:4], ts[4:6], ts[6:8],
		ts[9:11], ts[11:13], ts[13:15])
}
