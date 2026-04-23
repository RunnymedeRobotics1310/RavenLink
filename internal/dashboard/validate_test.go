package dashboard

import (
	"strings"
	"testing"
)

// TestValidateTargetEnabledURL — enabled=true requires non-empty URL.
// When only one of the two keys is sent (isolated toggle), skip the
// check so the operator isn't blocked from changing one field at a time.
func TestValidateTargetEnabledURL(t *testing.T) {
	cases := []struct {
		name    string
		data    map[string]any
		prefix  string
		wantErr string // substring to match; empty = expect no error
	}{
		{
			name:    "enabled_true_empty_url_rejects",
			data:    map[string]any{"ravenscope_enabled": true, "ravenscope_url": ""},
			prefix:  "ravenscope",
			wantErr: "ravenscope_url must be non-empty",
		},
		{
			name:   "enabled_true_with_url_accepts",
			data:   map[string]any{"ravenscope_enabled": true, "ravenscope_url": "https://scope.example"},
			prefix: "ravenscope",
		},
		{
			name:   "enabled_false_empty_url_accepts",
			data:   map[string]any{"ravenscope_enabled": false, "ravenscope_url": ""},
			prefix: "ravenscope",
		},
		{
			name:   "only_enabled_sent_skips_check",
			data:   map[string]any{"ravenscope_enabled": true},
			prefix: "ravenscope",
		},
		{
			name:   "only_url_sent_skips_check",
			data:   map[string]any{"ravenscope_url": ""},
			prefix: "ravenscope",
		},
		{
			name:    "brain_side_symmetric",
			data:    map[string]any{"ravenbrain_enabled": true, "ravenbrain_url": ""},
			prefix:  "ravenbrain",
			wantErr: "ravenbrain_url must be non-empty",
		},
		{
			name:    "whitespace_only_url_counts_as_empty",
			data:    map[string]any{"ravenscope_enabled": true, "ravenscope_url": "   "},
			prefix:  "ravenscope",
			wantErr: "must be non-empty",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateTargetEnabledURL(c.data, c.prefix)
			if c.wantErr == "" {
				if err != nil {
					t.Errorf("got err %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("got nil, want error containing %q", c.wantErr)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("error %q should contain %q", err.Error(), c.wantErr)
			}
		})
	}
}

// TestValidateConfigPost_TargetRule — the full validator surfaces the
// target-enabled-without-url error.
func TestValidateConfigPost_TargetRule(t *testing.T) {
	err := validateConfigPost(map[string]any{
		"ravenscope_enabled": true,
		"ravenscope_url":     "",
	})
	if err == nil {
		t.Fatal("expected error for enabled=true + empty url")
	}
	if !strings.Contains(err.Error(), "ravenscope_url") {
		t.Errorf("error should mention ravenscope_url: %v", err)
	}
}
