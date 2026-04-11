//go:build darwin

package tray

/*
#cgo darwin CFLAGS: -x objective-c -fobjc-arc
#cgo darwin LDFLAGS: -framework Cocoa
#include "nsapp_darwin.h"
#include <stdlib.h>
*/
import "C"

import "unsafe"

// installAppDelegate wires up a minimal NSApplicationDelegate so:
//  1. Clicking the Dock icon while the app is running re-opens the
//     dashboard URL instead of quitting the app.
//  2. The app doesn't self-terminate when it has no main windows
//     (which it never does — all UI is in the browser + menu bar).
func installAppDelegate(dashboardURL string) {
	c := C.CString(dashboardURL)
	defer C.free(unsafe.Pointer(c))
	C.RavenLinkInstallDelegate(c)
}
