#ifndef RAVENLINK_NSAPP_H
#define RAVENLINK_NSAPP_H

// Install an NSApplicationDelegate that:
//   1. Returns NO from applicationShouldTerminateAfterLastWindowClosed,
//      so clicking the Dock icon doesn't cause the app to quit (we
//      have no main window — without this, the default NSApp delegate
//      terminates the app when activated with no visible windows).
//   2. Opens the dashboard URL in the default browser when the user
//      re-clicks the Dock icon (applicationShouldHandleReopen).
//
// Call this once at startup, before systray.Run.
void RavenLinkInstallDelegate(const char *dashboardURL);

#endif
