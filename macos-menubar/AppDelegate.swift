// AppDelegate wires the status-bar item, a periodic refresh timer, and the
// dropdown menu. Kept in a separate file for clarity.

import AppKit
import Darwin

final class AppDelegate: NSObject, NSApplicationDelegate, NSMenuDelegate {
    private var statusItem: NSStatusItem!
    private let client = UsageClient()
    private var timer: Timer?
    // Daily buckets of the most recent days, for the "By date" breakdown.
    private var recentDays: [UsageBucket] = []
    private var parentTimer: Timer?
    // The dropdown lists these groups, each limited to its top rows.
    private let breakdowns = ["model", "runtime", "date"]
    private let topN = 3
    private let parentPID: pid_t? = {
        guard let raw = ProcessInfo.processInfo.environment["AMUX_PARENT_PID"],
              let value = Int32(raw) else {
            return nil
        }
        return pid_t(value)
    }()

    func applicationDidFinishLaunching(_ notification: Notification) {
        statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
        configureStatusButton()

        let menu = NSMenu()
        menu.delegate = self
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
        // The app logo is the only menu-bar content; figures live in the menu.
        button.image = appIcon()
        button.imagePosition = .imageOnly
        button.imageHugsTitle = true
        button.title = ""
        button.toolTip = "AgentMux"
    }

    // appIcon loads the AgentMux bundle icon scaled to the status-bar height.
    // Returns nil (icon-less) if unavailable.
    private func appIcon() -> NSImage? {
        guard let bundlePath = ProcessInfo.processInfo.environment["AMUX_APP_BUNDLE"] else {
            return nil
        }
        let icon = NSWorkspace.shared.icon(forFile: bundlePath)
        let side: CGFloat = 18
        let resized = NSImage(size: NSSize(width: side, height: side))
        resized.lockFocus()
        icon.draw(in: NSRect(x: 0, y: 0, width: side, height: side))
        resized.unlockFocus()
        return resized
    }

    private func refresh() {
        client.fetchDays(1) { [weak self] report in
            guard let self = self else { return }
            guard let report = report else {
                self.rebuildMenu(report: nil)
                return
            }
            self.rebuildMenu(report: report)
            // "By date" lists the most recent days, which today's report
            // does not hold.
            self.client.fetchDays(self.topN) { [weak self] recent in
                guard let self = self, let recent = recent else { return }
                self.recentDays = recent.buckets
                self.rebuildMenu(report: report)
            }
        }
    }

    private func rebuildMenu(report: UsageReport?) {
        let menu = NSMenu()
        menu.delegate = self
        if let report = report {
            for dimension in breakdowns {
                addBreakdown(to: menu, dimension: dimension, report: report)
            }
        } else {
            menu.addItem(NSMenuItem(title: "Daemon unreachable", action: nil, keyEquivalent: ""))
            menu.addItem(NSMenuItem(title: "Start: amux serve", action: nil, keyEquivalent: ""))
        }
        menu.addItem(NSMenuItem.separator())
        menu.addItem(NSMenuItem(title: "Open WebUI",
                                action: #selector(openWeb), keyEquivalent: ""))
        menu.addItem(NSMenuItem(title: "Quit",
                                action: #selector(quitAgentMux), keyEquivalent: "q"))
        for item in menu.items where item.action != nil { item.target = self }
        statusItem.menu = menu
    }

    // addBreakdown appends one grouped section (by model / runtime / date),
    // each limited to the top rows and sorted by cost.
    private func addBreakdown(to menu: NSMenu, dimension: String, report: UsageReport) {
        var rows: [(label: String, tokens: Int, records: Int, cost: Double)] = []
        var title = ""
        switch dimension {
        case "model":
            title = "By model"
            rows = report.by_model.map { ($0.model, $0.tokens, $0.records, $0.cost_usd) }
        case "runtime":
            title = "By agent framework"
            rows = report.by_runtime.map { ($0.runtime, $0.tokens, $0.records, $0.cost_usd) }
        case "date":
            title = "By date"
            // Buckets ascend by key; the most recent days are most useful.
            let days = recentDays.isEmpty ? report.buckets : recentDays
            rows = days.suffix(topN).reversed().map {
                ($0.key,
                 $0.totals.input_tokens + $0.totals.output_tokens,
                 $0.totals.records,
                 $0.totals.cost_usd)
            }
        default:
            return
        }
        if rows.isEmpty { return }
        menu.addItem(NSMenuItem.separator())
        menu.addItem(NSMenuItem(title: title, action: nil, keyEquivalent: ""))
        for row in rows.prefix(topN) {
            menu.addItem(NSMenuItem(title: "  \(row.label)", action: nil, keyEquivalent: ""))
            let detail = "\(fmtTokens(row.tokens)) tok · \(row.records) msg · \(fmtCost(row.cost))"
            let item = NSMenuItem(title: "      \(detail)", action: nil, keyEquivalent: "")
            item.isEnabled = false
            menu.addItem(item)
        }
    }

    // fmtTokens renders a raw token count as 1.2B / 900.7M / 12.3K / 42.
    private func fmtTokens(_ n: Int) -> String {
        let v = Double(n)
        if v >= 1e9 { return String(format: "%.2fB", v / 1e9) }
        if v >= 1e6 { return String(format: "%.2fM", v / 1e6) }
        if v >= 1e3 { return String(format: "%.2fK", v / 1e3) }
        return "\(n)"
    }

    // fmtCost renders a USD amount with grouping: "$18,019.74".
    private func fmtCost(_ usd: Double) -> String {
        "$" + grouped(usd, fraction: 2)
    }

    private func grouped(_ value: Double, fraction: Int) -> String {
        let fmt = NumberFormatter()
        fmt.numberStyle = .decimal
        fmt.minimumFractionDigits = fraction
        fmt.maximumFractionDigits = fraction
        return fmt.string(from: NSNumber(value: value)) ?? String(format: "%.\(fraction)f", value)
    }

    func menuWillOpen(_ menu: NSMenu) {
        // Reuse the desktop reopen path so a closed or minimised window is
        // restored whenever the status menu is opened, even without the daemon.
        guard parentPID != nil else { return }
        showAgentMux()
    }

    private func showAgentMux() {
        if let bundlePath = ProcessInfo.processInfo.environment["AMUX_APP_BUNDLE"],
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

    @objc private func quitAgentMux() {
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
