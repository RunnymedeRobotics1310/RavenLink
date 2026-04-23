package tray

import (
	"testing"

	"github.com/RunnymedeRobotics1310/RavenLink/internal/status"
)

func TestTargetLabel(t *testing.T) {
	cases := []struct {
		name        string
		displayName string
		reachable   bool
		pending     int
		want        string
	}{
		{"unreachable_zero", "RavenBrain", false, 0, "RavenBrain: --"},
		{"unreachable_backlog", "RavenBrain", false, 5, "RavenBrain: --"},
		{"reachable_idle", "RavenScope", true, 0, "RavenScope: Connected"},
		{"reachable_backlog", "RavenScope", true, 3, "RavenScope: 3 pending"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := targetLabel(c.displayName, c.reachable, c.pending)
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestFindTarget(t *testing.T) {
	targets := []status.UploadTargetStatus{
		{Name: "ravenbrain", Reachable: true},
		{Name: "ravenscope", Reachable: false},
	}
	if got := findTarget(targets, "ravenbrain"); got == nil || !got.Reachable {
		t.Errorf("findTarget(ravenbrain) = %+v", got)
	}
	if got := findTarget(targets, "ravenscope"); got == nil || got.Reachable {
		t.Errorf("findTarget(ravenscope) = %+v", got)
	}
	if got := findTarget(targets, "missing"); got != nil {
		t.Errorf("findTarget(missing) = %+v, want nil", got)
	}
	if got := findTarget(nil, "ravenbrain"); got != nil {
		t.Errorf("findTarget(nil slice) = %+v, want nil", got)
	}
}
