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
	mStatus     *systray.MenuItem
	mNT         *systray.MenuItem
	mOBS        *systray.MenuItem
	mRavenBrain *systray.MenuItem
	mRavenScope *systray.MenuItem
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

	// Status rows are informational — not Disable()'d because Windows
	// grays out the entire item including colored emoji, making the
	// connection dots invisible. Clicks are silently drained below.
	t.mStatus = systray.AddMenuItem("State: IDLE", "")
	t.mNT = systray.AddMenuItem("NT: --", "")
	t.mOBS = systray.AddMenuItem("OBS: --", "")
	// Both upload targets get their own rows. UpdateStatus hides any
	// target that isn't in status.UploadTargets on a given tick (i.e.
	// disabled or unconfigured) so the menu only shows what's active.
	t.mRavenBrain = systray.AddMenuItem("RavenBrain: --", "")
	t.mRavenScope = systray.AddMenuItem("RavenScope: --", "")
	t.mRavenBrain.Hide()
	t.mRavenScope.Hide()

	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit", "Quit RavenLink")

	go func() {
		for {
			select {
			case <-mOpen.ClickedCh:
				openBrowser(t.dashboardURL)
			case <-t.mStatus.ClickedCh:
				// informational — no action
			case <-t.mNT.ClickedCh:
				// informational — no action
			case <-t.mOBS.ClickedCh:
				// informational — no action
			case <-t.mRavenBrain.ClickedCh:
				// informational — no action
			case <-t.mRavenScope.ClickedCh:
				// informational — no action
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
	var targets []status.UploadTargetStatus

	st.Snapshot(func(s *status.Status) {
		ntConnected = s.NTConnected
		obsConnected = s.OBSConnected
		obsRecording = s.OBSRecording
		matchState = s.MatchState
		entriesWritten = s.EntriesWritten
		targets = append(targets, s.UploadTargets...)
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
		t.mNT.SetTitle(connLabel("NT", ntConnected))
	}
	if t.mOBS != nil {
		t.mOBS.SetTitle(connLabel("OBS", obsConnected))
	}
	applyTargetMenu(t.mRavenBrain, "RavenBrain", findTarget(targets, "ravenbrain"))
	applyTargetMenu(t.mRavenScope, "RavenScope", findTarget(targets, "ravenscope"))
}

// findTarget returns the first target whose Name matches (case-sensitive,
// the name is the canonical lowercase identifier from the uploader), or
// nil if not present in the current status snapshot.
func findTarget(targets []status.UploadTargetStatus, name string) *status.UploadTargetStatus {
	for i := range targets {
		if targets[i].Name == name {
			return &targets[i]
		}
	}
	return nil
}

// applyTargetMenu updates or hides a target's menu item based on whether
// it appears in the current status snapshot. A missing entry means the
// target is disabled/unconfigured; we hide the row instead of showing a
// stale "--" line.
func applyTargetMenu(item *systray.MenuItem, displayName string, t *status.UploadTargetStatus) {
	if item == nil {
		return
	}
	if t == nil {
		item.Hide()
		return
	}
	item.SetTitle(targetLabel(displayName, t.Reachable, t.FilesPending))
	item.Show()
}

// Stop tears down the system tray.
func (t *Tray) Stop() {
	systray.Quit()
}

// connLabel returns a status string for a connection row.
// Uses plain text instead of emoji because Win32 popup menus
// don't render colored emoji glyphs.
func connLabel(name string, connected bool) string {
	if connected {
		return name + ": Connected"
	}
	return name + ": --"
}

// targetLabel renders a single upload target's status line:
//
//	unreachable            → "<name>: --"
//	reachable, backlog     → "<name>: 3 pending"
//	reachable, caught up   → "<name>: Connected"
func targetLabel(name string, reachable bool, filesPending int) string {
	if !reachable {
		return name + ": --"
	}
	if filesPending > 0 {
		return fmt.Sprintf("%s: %d pending", name, filesPending)
	}
	return name + ": Connected"
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
