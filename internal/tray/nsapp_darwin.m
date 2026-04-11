//go:build darwin

#import <Cocoa/Cocoa.h>
#include "nsapp_darwin.h"
#include <string.h>

@interface RavenLinkAppDelegate : NSObject <NSApplicationDelegate>
@property(nonatomic, copy) NSString *dashboardURL;
@end

@implementation RavenLinkAppDelegate

// Don't quit the app when the "last window" is closed. We never have
// any windows — all UI is in the web dashboard and the menu bar. The
// default behavior of terminating the app is wrong for a background
// utility.
- (BOOL)applicationShouldTerminateAfterLastWindowClosed:(NSApplication *)sender {
    return NO;
}

// When the user clicks the Dock icon while we're already running,
// re-open the dashboard in the default browser. Returning NO tells
// AppKit we've handled the reopen event ourselves.
- (BOOL)applicationShouldHandleReopen:(NSApplication *)sender hasVisibleWindows:(BOOL)flag {
    if (self.dashboardURL != nil && self.dashboardURL.length > 0) {
        NSURL *url = [NSURL URLWithString:self.dashboardURL];
        if (url != nil) {
            [[NSWorkspace sharedWorkspace] openURL:url];
        }
    }
    return NO;
}

@end

static RavenLinkAppDelegate *gDelegate = nil;

void RavenLinkInstallDelegate(const char *dashboardURL) {
    // Ensure NSApp exists.
    [NSApplication sharedApplication];

    if (gDelegate == nil) {
        gDelegate = [[RavenLinkAppDelegate alloc] init];
    }
    if (dashboardURL != NULL) {
        gDelegate.dashboardURL = [NSString stringWithUTF8String:dashboardURL];
    }
    [NSApp setDelegate:gDelegate];
}
