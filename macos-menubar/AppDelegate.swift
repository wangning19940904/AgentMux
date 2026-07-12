// AppDelegate wires the status-bar item, a periodic refresh timer, and the
// dropdown menu. Kept in a separate file for clarity.

import AppKit
import Darwin

final class AppDelegate: NSObject, NSApplicationDelegate {
    private var statusItem: NSStatusItem!
    private let client = UsageClient()
    private var timer: Timer?
    private var parentTimer: Timer?
    private var settings = MenubarSettings.defaults
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
        // The app icon is shown as a leading image, followed by a single
        // animated emoji and the compact metrics as the button title.
        button.image = appIcon()
        button.imagePosition = .imageLeading
        button.imageHugsTitle = true
        button.title = "✨"
        button.toolTip = "AgentNexus"
    }

    // appIcon loads the AgentNexus bundle icon scaled to the status-bar height
    // so it sits next to the emoji. Returns nil (icon-less) if unavailable.
    private func appIcon() -> NSImage? {
        guard let bundlePath = ProcessInfo.processInfo.environment["ANX_APP_BUNDLE"] else {
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
        // Pull the latest preferences first so the icon and menu honor the
        // user's choices, then fetch the usage report.
        client.fetchSettings { [weak self] settings in
            guard let self = self else { return }
            self.settings = settings
            self.client.fetchDaily { [weak self] report in
                guard let self = self else { return }
                guard let report = report else {
                    self.statusItem.button?.title = "⚠️"
                    self.rebuildMenu(report: nil)
                    return
                }
                self.statusItem.button?.title = self.statusTitle(report: report)
                self.rebuildMenu(report: report)
            }
        }
    }

    // statusTitle builds the menu bar text: an animated icon reflecting current
    // burn intensity followed by the enabled compact metrics.
    private func statusTitle(report: UsageReport) -> String {
        let t = report.totals
        var parts: [String] = [iconFor(report: report)]
        if settings.show_cost {
            parts.append(fmtCostShort(t.cost_usd))
        }
        if settings.show_tokens {
            parts.append("\(fmtTokens(t.input_tokens))/\(fmtTokens(t.output_tokens))")
        }
        if settings.show_messages {
            parts.append("\(t.records)")
        }
        return parts.joined(separator: " ")
    }

    // iconFor maps the configured metric onto a 5-step emoji ladder using the
    // cost thresholds, then picks the flame/drop/custom glyph for that stage.
    private func iconFor(report: UsageReport) -> String {
        let value: Double
        switch settings.icon_metric {
        case "tokens":
            value = Double(report.totals.input_tokens + report.totals.output_tokens)
        case "messages":
            value = Double(report.totals.records)
        default:
            value = report.totals.cost_usd
        }
        let thresholds = iconThresholds()
        var stage = 0
        for th in thresholds where value >= th {
            stage += 1
        }
        stage = min(stage, 4)
        return iconStages()[stage]
    }

    // iconThresholds returns four ascending boundaries. For cost the configured
    // USD thresholds are used directly; other metrics are scaled so the ladder
    // still spans a useful range.
    private func iconThresholds() -> [Double] {
        let base = settings.cost_thresholds.isEmpty ? [0.01, 1, 10, 100] : settings.cost_thresholds
        switch settings.icon_metric {
        case "tokens":
            return [1_000, 100_000, 1_000_000, 10_000_000]
        case "messages":
            return [1, 10, 50, 200]
        default:
            return base
        }
    }

    private func iconStages() -> [String] {
        switch settings.icon_theme {
        case "drop":
            return ["💧", "💦", "⛲", "🌊", "☔"]
        case "custom":
            if settings.icon_stages.count == 5 { return settings.icon_stages }
            return ["💤", "✨", "🔥", "🔥", "🔥"]
        default: // flame
            return ["💤", "✨", "🔥", "🔥", "🔥"]
        }
    }

    private func rebuildMenu(report: UsageReport?) {
        let menu = NSMenu()
        if let report = report {
            let t = report.totals
            if settings.show_cost {
                menu.addItem(NSMenuItem(title: "Today: \(fmtCost(t.cost_usd))",
                                        action: nil, keyEquivalent: ""))
            }
            if settings.show_tokens {
                menu.addItem(NSMenuItem(title: "Tokens in/out: \(fmtTokens(t.input_tokens))/\(fmtTokens(t.output_tokens))",
                                        action: nil, keyEquivalent: ""))
            }
            if settings.show_messages {
                menu.addItem(NSMenuItem(title: "Messages: \(t.records)",
                                        action: nil, keyEquivalent: ""))
            }
            for dimension in settings.breakdowns {
                addBreakdown(to: menu, dimension: dimension, report: report)
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

    // addBreakdown appends one grouped section (by model / runtime / date),
    // each limited to the configured Top N rows and sorted by cost.
    private func addBreakdown(to menu: NSMenu, dimension: String, report: UsageReport) {
        let topN = max(settings.top_n, 1)
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
            rows = report.buckets.suffix(topN).reversed().map {
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

    // fmtCost renders a USD amount with grouping and, when enabled, the CNY
    // equivalent at the fixed configured rate: "$18,019.74 · ¥129,742".
    private func fmtCost(_ usd: Double) -> String {
        var out = "$" + grouped(usd, fraction: 2)
        if settings.show_cny {
            out += " · ¥" + grouped(usd * settings.cny_rate, fraction: 0)
        }
        return out
    }

    // fmtCostShort is the compact menu-bar variant: large amounts collapse to
    // $18.0K / $1.2M so the status item stays narrow.
    private func fmtCostShort(_ usd: Double) -> String {
        if usd >= 1e6 { return String(format: "$%.1fM", usd / 1e6) }
        if usd >= 1e3 { return String(format: "$%.1fK", usd / 1e3) }
        return String(format: "$%.2f", usd)
    }

    private func grouped(_ value: Double, fraction: Int) -> String {
        let fmt = NumberFormatter()
        fmt.numberStyle = .decimal
        fmt.minimumFractionDigits = fraction
        fmt.maximumFractionDigits = fraction
        return fmt.string(from: NSNumber(value: value)) ?? String(format: "%.\(fraction)f", value)
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
