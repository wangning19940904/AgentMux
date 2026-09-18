//go:build desktop && darwin

#import <Cocoa/Cocoa.h>
#import "dock_darwin.h"

// Preserve Wails' delegates (including fullscreen and quit handling), adding
// Dock visibility only to the close-to-background and reopen paths.
@interface AgentMuxDockDelegate : NSObject <NSWindowDelegate, NSApplicationDelegate>
@property(nonatomic, strong) id<NSWindowDelegate> windowDelegate;
@property(nonatomic, strong) id<NSApplicationDelegate> applicationDelegate;
@property(nonatomic, weak) NSWindow *window;
@property(nonatomic) BOOL closedToMenuBar;
@end

@implementation AgentMuxDockDelegate

- (BOOL)respondsToSelector:(SEL)selector {
    return [super respondsToSelector:selector] ||
           [self.windowDelegate respondsToSelector:selector] ||
           [self.applicationDelegate respondsToSelector:selector];
}

- (id)forwardingTargetForSelector:(SEL)selector {
    if ([self.windowDelegate respondsToSelector:selector]) {
        return self.windowDelegate;
    }
    return self.applicationDelegate;
}

- (BOOL)windowShouldClose:(NSWindow *)window {
    self.closedToMenuBar = YES;
    BOOL shouldClose = YES;
    if ([self.windowDelegate respondsToSelector:_cmd]) {
        shouldClose = [self.windowDelegate windowShouldClose:window];
    }
    if (shouldClose) {
        self.closedToMenuBar = NO;
    }
    return shouldClose;
}

- (void)hideDock:(NSNotification *)notification {
    // Wails' HideWindowOnClose calls NSApp.hide, which completes asynchronously.
    // Wait for the actual hide so changing activation policy cannot race it.
    if (self.closedToMenuBar) {
        [NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
    }
}

- (BOOL)applicationShouldHandleReopen:(NSApplication *)application hasVisibleWindows:(BOOL)visible {
    // Accessory applications are not reliably unhidden by Launch Services.
    // Explicitly restore the existing window for Finder and the menu-bar item.
    [self restoreDock:nil];
    [application unhide:nil];
    if (self.window.miniaturized) {
        [self.window deminiaturize:nil];
    }
    [self.window makeKeyAndOrderFront:nil];
    [application activateIgnoringOtherApps:YES];
    return NO;
}

- (void)restoreDock:(NSNotification *)notification {
    if (self.closedToMenuBar) {
        self.closedToMenuBar = NO;
        [NSApp setActivationPolicy:NSApplicationActivationPolicyRegular];
    }
}

@end

void agentmuxConfigureDock(void) {
    // OnStartup runs on a Go worker; every AppKit operation must run on the
    // main queue. Wails has already created its main window before OnStartup.
    dispatch_async(dispatch_get_main_queue(), ^{
        static AgentMuxDockDelegate *dockDelegate;
        if (dockDelegate != nil) {
            return;
        }
        NSWindow *window = NSApp.mainWindow;
        if (window == nil) {
            for (NSWindow *candidate in NSApp.windows) {
                if (candidate.canBecomeMainWindow) {
                    window = candidate;
                    break;
                }
            }
        }
        if (window == nil) {
            return;
        }
        dockDelegate = [AgentMuxDockDelegate new];
        dockDelegate.windowDelegate = window.delegate;
        dockDelegate.applicationDelegate = NSApp.delegate;
        dockDelegate.window = window;
        window.delegate = dockDelegate;
        NSApp.delegate = dockDelegate;

        NSNotificationCenter *center = NSNotificationCenter.defaultCenter;
        [center addObserver:dockDelegate selector:@selector(hideDock:)
                       name:NSApplicationDidHideNotification object:NSApp];
        // Finder / the menu-bar helper reopen the hidden application. A second
        // executable launch instead uses Wails' WindowShow; cover both paths.
        [center addObserver:dockDelegate selector:@selector(restoreDock:)
                       name:NSApplicationWillUnhideNotification object:NSApp];
        [center addObserver:dockDelegate selector:@selector(restoreDock:)
                       name:NSWindowDidBecomeKeyNotification object:window];
    });
}
