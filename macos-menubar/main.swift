// AgentNexus macOS menu bar app.
//
// A lightweight SwiftUI/AppKit status-bar item that polls the local
// AgentNexus daemon (http://127.0.0.1:8765/api/v1/usage) and shows today's
// estimated token cost in the menu bar, with a dropdown breakdown. Inspired by
// CodeBurn / cc-statistics menu bar panels, but it reads from our own daemon so
// there is a single source of truth across CLI, WebUI and desktop.
//
// Build (macOS, requires Xcode command line tools):
//   swiftc -O -o AgentNexusMenuBar main.swift -framework AppKit -framework SwiftUI
// Then run ./AgentNexusMenuBar (the daemon must be running: `anx serve`).

import AppKit
import SwiftUI

let daemonBase = ProcessInfo.processInfo.environment["ANX_ADDR"] ?? "http://127.0.0.1:8765"

// UsageTotals mirrors the daemon's JSON usage totals.
struct UsageTotals: Decodable {
    let cost_usd: Double
    let input_tokens: Int
    let output_tokens: Int
    let records: Int
}

struct ModelStat: Decodable {
    let model: String
    let tokens: Int
    let cost_usd: Double
}

struct UsageReport: Decodable {
    let period: String
    let totals: UsageTotals
    let by_model: [ModelStat]
}

// UsageClient fetches the daily report from the daemon.
final class UsageClient {
    func fetchDaily(completion: @escaping (UsageReport?) -> Void) {
        guard let url = URL(string: "\(daemonBase)/api/v1/usage?period=daily") else {
            completion(nil); return
        }
        URLSession.shared.dataTask(with: url) { data, _, _ in
            guard let data = data,
                  let report = try? JSONDecoder().decode(UsageReport.self, from: data) else {
                completion(nil); return
            }
            DispatchQueue.main.async { completion(report) }
        }.resume()
    }
}

// Entry point. Top-level executable code is only permitted in main.swift.
let app = NSApplication.shared
let delegate = AppDelegate()
app.delegate = delegate
app.setActivationPolicy(.accessory) // menu-bar only, no dock icon
app.run()
