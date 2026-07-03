// AppDelegate wires the status-bar item, a periodic refresh timer, and the
// dropdown menu. Kept in a separate file for clarity.

import AppKit

final class AppDelegate: NSObject, NSApplicationDelegate {
    private var statusItem: NSStatusItem!
    private let client = UsageClient()
    private var timer: Timer?

    func applicationDidFinishLaunching(_ notification: Notification) {
        statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
        statusItem.button?.title = "ANX …"

        let menu = NSMenu()
        menu.addItem(NSMenuItem(title: "Refreshing…", action: nil, keyEquivalent: ""))
        statusItem.menu = menu

        refresh()
        timer = Timer.scheduledTimer(withTimeInterval: 60, repeats: true) { [weak self] _ in
            self?.refresh()
        }
    }

    private func refresh() {
        client.fetchDaily { [weak self] report in
            guard let self = self else { return }
            guard let report = report else {
                self.statusItem.button?.title = "ANX ⚠"
                self.rebuildMenu(report: nil)
                return
            }
            let cost = String(format: "$%.2f", report.totals.cost_usd)
            self.statusItem.button?.title = "ANX \(cost)"
            self.rebuildMenu(report: report)
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
        menu.addItem(NSMenuItem(title: "Open WebUI",
                                action: #selector(openWeb), keyEquivalent: "o"))
        menu.addItem(NSMenuItem(title: "Quit",
                                action: #selector(NSApplication.terminate(_:)), keyEquivalent: "q"))
        for item in menu.items where item.action != nil { item.target = self }
        statusItem.menu = menu
    }

    @objc private func openWeb() {
        if let url = URL(string: daemonBase) {
            NSWorkspace.shared.open(url)
        }
    }
}
