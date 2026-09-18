//go:build desktop && darwin

// Compile the production bridge into a real Wails app without starting the
// AgentMux daemon, touching user settings, or replacing the installed app.
#import "../../dock_darwin.m"
#import <stdlib.h>

static NSWindow *testWindow;
static int step, retries, completed;
static void tick(void);

static void check(BOOL pass, const char *label) {
    printf("%s: %s\n", pass ? "PASS" : "FAIL", label);
    fflush(stdout);
    if (!pass) exit(1);
}

static void nextTick(void) {
    dispatch_after(dispatch_time(DISPATCH_TIME_NOW, NSEC_PER_SEC), dispatch_get_main_queue(), ^{ tick(); });
}

static BOOL expectState(BOOL pass, const char *label) {
    // Hide/unhide, Dock updates, and minimise animations are asynchronous.
    if (!pass && ++retries < 10) {
        step--;
        nextTick();
        return NO;
    }
    check(pass, label);
    retries = 0;
    return YES;
}

static BOOL regular(void) {
    return NSApp.activationPolicy == NSApplicationActivationPolicyRegular;
}

static void tick(void) {
    switch (step++) {
    case 0:
        testWindow = NSApp.mainWindow;
        if (!expectState(testWindow != nil && regular(), "initial window has Dock presence")) return;
        check([testWindow.delegate respondsToSelector:@selector(windowDidExitFullScreen:)], "Wails fullscreen delegate is forwarded");
        [testWindow performClose:nil];
        break;
    case 1:
        if (!expectState(NSApp.hidden && NSApp.activationPolicy == NSApplicationActivationPolicyAccessory, "red close hides app and removes Dock presence")) return;
        check([NSWorkspace.sharedWorkspace openURL:NSBundle.mainBundle.bundleURL], "menu-bar/Finder reopen request accepted");
        break;
    case 2:
        if (!expectState(!NSApp.hidden && regular() && testWindow.visible, "bundle reopen restores window and Dock")) return;
        [testWindow miniaturize:nil];
        break;
    case 3:
        if (!expectState(testWindow.miniaturized && regular(), "minimise preserves Dock presence")) return;
        [testWindow deminiaturize:nil];
        break;
    case 4:
        if (!expectState(!testWindow.miniaturized && regular(), "restoring a minimised window preserves Dock presence")) return;
        [NSApp hide:nil];
        break;
    case 5:
        if (!expectState(NSApp.hidden && regular(), "ordinary Hide preserves Dock presence")) return;
        [NSApp unhide:nil];
        [NSApp activateIgnoringOtherApps:YES];
        break;
    case 6:
        if (!expectState(!NSApp.hidden && regular(), "ordinary Unhide preserves Dock presence")) return;
        [testWindow performClose:nil];
        break;
    case 7: {
        if (!expectState(NSApp.hidden && NSApp.activationPolicy == NSApplicationActivationPolicyAccessory, "repeated close removes Dock presence")) return;
        NSTask *task = [NSTask new];
        task.executableURL = NSBundle.mainBundle.executableURL;
        check([task launchAndReturnError:nil], "second executable launch accepted");
        break;
    }
    case 8:
        if (!expectState(!NSApp.hidden && regular() && testWindow.visible, "second instance restores window and Dock")) return;
        completed = 1;
        [NSApp terminate:nil];
        return;
    }
    nextTick();
}

void agentmuxRunDockSmoke(void) {
    dispatch_async(dispatch_get_main_queue(), ^{ tick(); });
}

int agentmuxDockSmokeCompleted(void) {
    return completed;
}
