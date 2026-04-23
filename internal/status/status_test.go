package status

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

// TestNewHasEmptyTargetsSlice — the initial UploadTargets must be
// non-nil so the dashboard JS can iterate safely.
func TestNewHasEmptyTargetsSlice(t *testing.T) {
	s := New()
	if s.UploadTargets == nil {
		t.Fatal("UploadTargets is nil; want empty slice")
	}
	if len(s.UploadTargets) != 0 {
		t.Errorf("UploadTargets length = %d; want 0", len(s.UploadTargets))
	}
}

// TestJSONEmptyArrayNotNull — marshaling a Status with no targets must
// emit `"upload_targets":[]`, not `"upload_targets":null`.
func TestJSONEmptyArrayNotNull(t *testing.T) {
	s := New()
	b, err := s.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON: %v", err)
	}
	if !strings.Contains(string(b), `"upload_targets":[]`) {
		t.Errorf("JSON should contain upload_targets:[]\n%s", string(b))
	}
	if strings.Contains(string(b), `"upload_targets":null`) {
		t.Error("JSON should not contain null for upload_targets")
	}
}

// TestJSONWithTwoTargets — populated targets serialize with the
// dashboard contract fields intact.
func TestJSONWithTwoTargets(t *testing.T) {
	s := New()
	s.Update(func(st *Status) {
		st.UploadTargets = []UploadTargetStatus{
			{
				Name:               "ravenbrain",
				Enabled:            true,
				Reachable:          true,
				FilesPending:       2,
				FilesUploaded:      7,
				CurrentlyUploading: false,
				LastResult:         "OK: abc.jsonl",
			},
			{
				Name:       "ravenscope",
				Enabled:    true,
				Reachable:  false,
				LastResult: "HTTP 503",
			},
		}
	})

	b, err := s.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON: %v", err)
	}
	var parsed struct {
		Targets []map[string]any `json:"upload_targets"`
	}
	if err := json.Unmarshal(b, &parsed); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(parsed.Targets) != 2 {
		t.Fatalf("targets len = %d, want 2", len(parsed.Targets))
	}
	if parsed.Targets[0]["name"] != "ravenbrain" {
		t.Errorf("first target name: %v", parsed.Targets[0]["name"])
	}
	if parsed.Targets[0]["files_uploaded"].(float64) != 7 {
		t.Errorf("files_uploaded: %v", parsed.Targets[0]["files_uploaded"])
	}
	if parsed.Targets[1]["reachable"].(bool) {
		t.Error("second target should be unreachable")
	}
}

// TestConcurrentReadWrite — Update + ToJSON are safe under -race.
func TestConcurrentReadWrite(t *testing.T) {
	s := New()
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s.Update(func(st *Status) {
				st.EntriesWritten += i
				st.UploadTargets = append(st.UploadTargets, UploadTargetStatus{
					Name:          "t",
					Enabled:       true,
					FilesUploaded: i,
				})
			})
		}(i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = s.ToJSON()
		}()
	}
	wg.Wait()
}
