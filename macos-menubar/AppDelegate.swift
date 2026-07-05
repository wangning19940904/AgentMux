// AppDelegate wires the status-bar item, a periodic refresh timer, and the
// dropdown menu. Kept in a separate file for clarity.

import AppKit
import Darwin

final class AppDelegate: NSObject, NSApplicationDelegate {
    private var statusItem: NSStatusItem!
    private let client = UsageClient()
    private var timer: Timer?
    private var parentTimer: Timer?
    private let parentPID: pid_t? = {
        guard let raw = ProcessInfo.processInfo.environment["ANX_PARENT_PID"],
              let value = Int32(raw) else {
            return nil
        }
        return pid_t(value)
    }()

    func applicationDidFinishLaunching(_ notification: Notification) {
        statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
        configureStatusButton()
        setStatusText("…")

        let menu = NSMenu()
        menu.addItem(NSMenuItem(title: "Refreshing…", action: nil, keyEquivalent: ""))
        statusItem.menu = menu

        refresh()
        timer = Timer.scheduledTimer(withTimeInterval: 60, repeats: true) { [weak self] _ in
            self?.refresh()
        }
        if parentPID != nil {
            parentTimer = Timer.scheduledTimer(withTimeInterval: 5, repeats: true) { [weak self] _ in
                self?.quitIfParentExited()
            }
        }
    }

    private func configureStatusButton() {
        guard let button = statusItem.button else { return }
        if let image = NSImage(systemSymbolName: "bolt.circle.fill", accessibilityDescription: "AgentNexus") {
            image.isTemplate = true
            button.image = image
            button.imagePosition = .imageLeft
        }
        button.toolTip = "AgentNexus"
    }

    private func refresh() {
        client.fetchDaily { [weak self] report in
            guard let self = self else { return }
            guard let report = report else {
                self.setStatusText("⚠")
                self.rebuildMenu(report: nil)
                return
            }
            let cost = String(format: "$%.2f", report.totals.cost_usd)
            self.setStatusText(cost)
            self.rebuildMenu(report: report)
        }
    }

    private func setStatusText(_ text: String) {
        if statusItem.button?.image == nil {
            statusItem.button?.title = "ANX \(text)"
        } else {
            statusItem.button?.title = " \(text)"
        }
    }

    private func rebuildMenu(report: UsageReport?) {
        let menu = NSMenu()
        if let report = report {
            let t = report.totals
            menu.addItem(NSMenuItem(title: "Today: $\(String(format: "%.2f", t.cost_usd))",
                                    action: nil, keyEquivalent: ""))
            menu.addItem(NSMenuItem(title: "Tokens in/out: \(t.input_tokens)/\(t.output_tokens)",
                                    action: nil, keyEquivalent: ""))
            menu.addItem(NSMenuItem.separator())
            menu.addItem(NSMenuItem(title: "By model", action: nil, keyEquivalent: ""))
            for m in report.by_model.prefix(6) {
                menu.addItem(NSMenuItem(title: "  \(m.model): $\(String(format: "%.2f", m.cost_usd))",
                                        action: nil, keyEquivalent: ""))
            }
        } else {
            menu.addItem(NSMenuItem(title: "Daemon unreachable", action: nil, keyEquivalent: ""))
            menu.addItem(NSMenuItem(title: "Start: anx serve", action: nil, keyEquivalent: ""))
        }
        menu.addItem(NSMenuItem.separator())
        if parentPID != nil {
            menu.addItem(NSMenuItem(title: "Show AgentNexus",
                                    action: #selector(showAgentNexus), keyEquivalent: "o"))
        }
        menu.addItem(NSMenuItem(title: "Open WebUI",
                                action: #selector(openWeb), keyEquivalent: ""))
        menu.addItem(NSMenuItem(title: "Quit",
                                action: #selector(quitAgentNexus), keyEquivalent: "q"))
        for item in menu.items where item.action != nil { item.target = self }
        statusItem.menu = menu
    }

    @objc private func showAgentNexus() {
        if let bundlePath = ProcessInfo.processInfo.environment["ANX_APP_BUNDLE"],
           NSWorkspace.shared.open(URL(fileURLWithPath: bundlePath)) {
            return
        }
        if let pid = parentPID,
           let app = NSRunningApplication(processIdentifier: pid) {
            app.unhide()
            app.activate(options: [.activateAllWindows])
        }
    }

    @objc private func openWeb() {
        if let url = URL(string: daemonBase) {
            NSWorkspace.shared.open(url)
        }
    }

    @objc private func quitAgentNexus() {
        if let pid = parentPID {
            kill(pid, SIGTERM)
        }
        NSApplication.shared.terminate(nil)
    }

    private func quitIfParentExited() {
        guard let pid = parentPID else { return }
        if kill(pid, 0) != 0 {
            NSApplication.shared.terminate(nil)
        }
    }
}
