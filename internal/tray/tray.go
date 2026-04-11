// Package tray provides a system tray icon with status colors and a
// right-click menu for quick access to the RavenLink dashboard.
package tray

import (
	"fmt"
	"log/slog"
	"os/exec"
	"runtime"
	"sync"

	"fyne.io/systray"

	"github.com/RunnymedeRobotics1310/RavenLink/internal/status"
)

// Tray manages the system tray icon and its menu.
type Tray struct {
	mu           sync.Mutex
	dashboardURL string
	quitCh       chan<- struct{}
	quitOnce     sync.Once
	currentColor string

	// Menu items we need to update dynamically.
	mStatus *systray.MenuItem
	mNT     *systray.MenuItem
	mOBS    *systray.MenuItem
	mBrain  *systray.MenuItem
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
//
// On macOS, systray.Run MUST be called from the main goroutine, and
// the process must be a proper GUI app (bundled as .app). We also
// install a small NSApplicationDelegate via CGo so:
//   - Dock re-clicks re-open the dashboard instead of quitting the app
//   - The app doesn't self-terminate on "last window closed" (we
//     never have windows — the UI is in the browser and menu bar).
func (t *Tray) Start() {
	slog.Info("tray: starting systray event loop")
	installAppDelegate(t.dashboardURL)
	systray.Run(t.onReady, t.onExit)
	slog.Info("tray: systray event loop exited")
}

func (t *Tray) onReady() {
	slog.Info("tray: onReady fired, installing icon and menu")
	setTrayIcon("gray")
	// Deliberately no SetTitle — the menu bar should show only the
	// icon, not the text "RavenLink" next to it.
	systray.SetTooltip("RavenLink")

	mOpen := systray.AddMenuItem("Open Dashboard", "Open the web dashboard in a browser")
	systray.AddSeparator()

	t.mStatus = systray.AddMenuItem("State: IDLE", "")
	t.mStatus.Disable()
	t.mNT = systray.AddMenuItem("⚪ NT", "")
	t.mNT.Disable()
	t.mOBS = systray.AddMenuItem("⚪ OBS", "")
	t.mOBS.Disable()
	t.mBrain = systray.AddMenuItem("⚪ RavenBrain", "")
	t.mBrain.Disable()

	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit", "Quit RavenLink")

	go func() {
		for {
			select {
			case <-mOpen.ClickedCh:
				openBrowser(t.dashboardURL)
			case <-mQuit.ClickedCh:
				t.quitOnce.Do(func() {
					if t.quitCh != nil {
						close(t.quitCh)
					}
				})
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

	var ntConnected, obsConnected, obsRecording, brainConfigured bool
	var matchState string
	var entriesWritten, filesPending int

	st.Snapshot(func(s *status.Status) {
		ntConnected = s.NTConnected
		obsConnected = s.OBSConnected
		obsRecording = s.OBSRecording
		matchState = s.MatchState
		entriesWritten = s.EntriesWritten
		brainConfigured = s.RavenBrainReachable
		filesPending = s.FilesPending
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
		setTrayIcon(newColor)
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

	// Update menu items. Connection rows use colored-dot emoji for
	// an at-a-glance status read; text-only items (State) stay as text.
	if t.mStatus != nil {
		t.mStatus.SetTitle("State: " + matchState)
	}
	if t.mNT != nil {
		t.mNT.SetTitle(connDot(ntConnected) + " NT")
	}
	if t.mOBS != nil {
		t.mOBS.SetTitle(connDot(obsConnected) + " OBS")
	}
	if t.mBrain != nil {
		t.mBrain.SetTitle(brainDot(brainConfigured, filesPending) + " RavenBrain")
	}
}

// Stop tears down the system tray.
func (t *Tray) Stop() {
	systray.Quit()
}

// connDot returns a green circle for a live connection and a
// neutral white circle otherwise. Disconnected is a common/expected
// idle state (robot off, OBS not running yet), so we deliberately
// don't flag it as red — we just don't light it up.
func connDot(connected bool) string {
	if connected {
		return "🟢"
	}
	return "⚪"
}

// brainDot is like connDot but adds a yellow "work pending" state:
//
//	not configured       → ⚪
//	configured, backlog  → 🟡  (files in pending/ waiting to upload)
//	configured, caught up → 🟢
func brainDot(configured bool, filesPending int) string {
	if !configured {
		return "⚪"
	}
	if filesPending > 0 {
		return "🟡"
	}
	return "🟢"
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
		return
	}
	// Reap the child process asynchronously so it doesn't become a zombie.
	go func() { _ = cmd.Wait() }()
}
