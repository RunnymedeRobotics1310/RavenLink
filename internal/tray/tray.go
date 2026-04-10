// Package tray provides a system tray icon with status colors and a
// right-click menu for quick access to the RavenLink dashboard.
package tray

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log/slog"
	"math"
	"os/exec"
	"runtime"
	"sync"

	"fyne.io/systray"

	"github.com/RunnymedeRobotics1310/RavenLink/internal/status"
)

// predefined icon colours matching the Python implementation
var iconColors = map[string]color.RGBA{
	"green":  {R: 34, G: 197, B: 94, A: 255},
	"yellow": {R: 234, G: 179, B: 8, A: 255},
	"red":    {R: 239, G: 68, B: 68, A: 255},
	"gray":   {R: 156, G: 163, B: 175, A: 255},
}

// makeIcon renders a 64x64 PNG of a coloured circle on a transparent
// background, matching the Python _make_icon function.
func makeIcon(name string) []byte {
	const size = 64
	const cx, cy = size / 2, size / 2
	const r = (size - 8) / 2 // 4px padding each side

	fill, ok := iconColors[name]
	if !ok {
		fill = iconColors["gray"]
	}

	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dx := float64(x - cx)
			dy := float64(y - cy)
			if math.Sqrt(dx*dx+dy*dy) <= float64(r) {
				img.SetRGBA(x, y, fill)
			}
		}
	}

	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// Tray manages the system tray icon and its menu.
type Tray struct {
	mu           sync.Mutex
	dashboardURL string
	quitCh       chan<- struct{}
	currentColor string

	// Menu items we need to update dynamically.
	mStatus *systray.MenuItem
	mNT     *systray.MenuItem
	mOBS    *systray.MenuItem
}

// New creates a new Tray. dashboardURL is opened in the browser when
// the user clicks "Open Dashboard". Sending on quitCh signals the
// application to shut down.
func New(dashboardURL string, quitCh chan<- struct{}) *Tray {
	return &Tray{
		dashboardURL: dashboardURL,
		quitCh:       quitCh,
		currentColor: "gray",
	}
}

// Start initialises the system tray icon. It blocks until the tray
// exits, so call it from a dedicated goroutine (or use systray.Run
// which itself blocks).
func (t *Tray) Start() {
	systray.Run(t.onReady, t.onExit)
}

func (t *Tray) onReady() {
	systray.SetIcon(makeIcon("gray"))
	systray.SetTitle("RavenLink")
	systray.SetTooltip("RavenLink")

	mOpen := systray.AddMenuItem("Open Dashboard", "Open the web dashboard in a browser")
	systray.AddSeparator()

	t.mStatus = systray.AddMenuItem("State: IDLE", "")
	t.mStatus.Disable()
	t.mNT = systray.AddMenuItem("NT: Unknown", "")
	t.mNT.Disable()
	t.mOBS = systray.AddMenuItem("OBS: Unknown", "")
	t.mOBS.Disable()

	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit", "Quit RavenLink")

	go func() {
		for {
			select {
			case <-mOpen.ClickedCh:
				openBrowser(t.dashboardURL)
			case <-mQuit.ClickedCh:
				if t.quitCh != nil {
					close(t.quitCh)
				}
				systray.Quit()
				return
			}
		}
	}()
}

func (t *Tray) onExit() {
	slog.Info("system tray exited")
}

// UpdateStatus refreshes the tray icon colour and menu text based on
// the current bridge status.
func (t *Tray) UpdateStatus(st *status.Status) {
	t.mu.Lock()
	defer t.mu.Unlock()

	var ntConnected, obsConnected, obsRecording bool
	var matchState string
	var entriesWritten int

	st.Snapshot(func(s *status.Status) {
		ntConnected = s.NTConnected
		obsConnected = s.OBSConnected
		obsRecording = s.OBSRecording
		matchState = s.MatchState
		entriesWritten = s.EntriesWritten
	})

	// Determine colour.
	var newColor string
	switch {
	case ntConnected && obsConnected:
		newColor = "green"
	case ntConnected || obsConnected:
		newColor = "yellow"
	default:
		newColor = "red"
	}

	if newColor != t.currentColor {
		t.currentColor = newColor
		systray.SetIcon(makeIcon(newColor))
	}

	// Build tooltip.
	tooltip := matchState
	if entriesWritten > 0 {
		tooltip += fmt.Sprintf(" | %d entries", entriesWritten)
	}
	if obsRecording {
		tooltip += " | REC"
	}
	systray.SetTooltip("RavenLink: " + tooltip)

	// Update menu items.
	if t.mStatus != nil {
		t.mStatus.SetTitle("State: " + matchState)
	}
	if t.mNT != nil {
		t.mNT.SetTitle("NT: " + connText(ntConnected))
	}
	if t.mOBS != nil {
		t.mOBS.SetTitle("OBS: " + connText(obsConnected))
	}
}

// Stop tears down the system tray.
func (t *Tray) Stop() {
	systray.Quit()
}

func connText(connected bool) string {
	if connected {
		return "Connected"
	}
	return "Disconnected"
}

// openBrowser opens the given URL in the default browser.
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		slog.Warn("failed to open browser", "url", url, "err", err)
	}
}
