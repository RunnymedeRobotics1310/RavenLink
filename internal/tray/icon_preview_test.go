package tray

import (
	"os"
	"testing"
)

// TestRenderIconPNG_preview writes sample icons to /tmp for visual
// inspection. Run with `go test -run IconPreview -v` then open the
// files. Not part of the default test suite.
func TestRenderIconPNG_preview(t *testing.T) {
	if os.Getenv("TRAY_PREVIEW") == "" {
		t.Skip("set TRAY_PREVIEW=1 to generate preview icons in /tmp")
	}
	for _, name := range []string{"green", "yellow", "red", "gray"} {
		data := renderIconPNG(name)
		path := "/tmp/ravenlink-" + name + ".png"
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		t.Logf("wrote %s (%d bytes)", path, len(data))
	}
}

// TestMakeIcon_NonEmpty sanity-checks that makeIcon returns some bytes.
func TestMakeIcon_NonEmpty(t *testing.T) {
	for _, name := range []string{"green", "yellow", "red", "gray", "unknown"} {
		data := makeIcon(name)
		if len(data) == 0 {
			t.Errorf("makeIcon(%q) returned no bytes", name)
		}
	}
}
