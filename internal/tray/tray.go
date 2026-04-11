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
// the process must be a proper GUI app (bundled as .app, or with
// NSApplication.activationPolicy set to accessory). fyne.io/systray
// handles the latter internally via a call to TransformProcessType.
func (t *Tray) Start() {
	slog.Info("tray: starting systray event loop")
	systray.Run(t.onReady, t.onExit)
	slog.Info("tray: systray event loop exited")
}

func (t *Tray) onReady() {
	slog.Info("tray: onReady fired, installing icon and menu")
	setTrayIcon("gray")
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
		return
	}
	// Reap the child process asynchronously so it doesn't become a zombie.
	go func() { _ = cmd.Wait() }()
}
