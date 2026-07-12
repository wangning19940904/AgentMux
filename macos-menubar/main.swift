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
    let cache_read_tokens: Int
    let records: Int

    enum CodingKeys: String, CodingKey {
        case cost_usd, input_tokens, output_tokens, cache_read_tokens, records
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        cost_usd = try c.decodeIfPresent(Double.self, forKey: .cost_usd) ?? 0
        input_tokens = try c.decodeIfPresent(Int.self, forKey: .input_tokens) ?? 0
        output_tokens = try c.decodeIfPresent(Int.self, forKey: .output_tokens) ?? 0
        cache_read_tokens = try c.decodeIfPresent(Int.self, forKey: .cache_read_tokens) ?? 0
        records = try c.decodeIfPresent(Int.self, forKey: .records) ?? 0
    }
}

struct ModelStat: Decodable {
    let model: String
    let tokens: Int
    let cost_usd: Double
    let records: Int

    enum CodingKeys: String, CodingKey { case model, tokens, cost_usd, records }
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        model = try c.decode(String.self, forKey: .model)
        tokens = try c.decodeIfPresent(Int.self, forKey: .tokens) ?? 0
        cost_usd = try c.decodeIfPresent(Double.self, forKey: .cost_usd) ?? 0
        records = try c.decodeIfPresent(Int.self, forKey: .records) ?? 0
    }
}

struct AgentStat: Decodable {
    let agent: String
    let tokens: Int
    let cost_usd: Double
    let records: Int

    enum CodingKeys: String, CodingKey { case agent, tokens, cost_usd, records }
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        agent = try c.decode(String.self, forKey: .agent)
        tokens = try c.decodeIfPresent(Int.self, forKey: .tokens) ?? 0
        cost_usd = try c.decodeIfPresent(Double.self, forKey: .cost_usd) ?? 0
        records = try c.decodeIfPresent(Int.self, forKey: .records) ?? 0
    }
}

struct RuntimeStat: Decodable {
    let runtime: String
    let tokens: Int
    let cost_usd: Double
    let records: Int

    enum CodingKeys: String, CodingKey { case runtime, tokens, cost_usd, records }
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        runtime = try c.decode(String.self, forKey: .runtime)
        tokens = try c.decodeIfPresent(Int.self, forKey: .tokens) ?? 0
        cost_usd = try c.decodeIfPresent(Double.self, forKey: .cost_usd) ?? 0
        records = try c.decodeIfPresent(Int.self, forKey: .records) ?? 0
    }
}

// Bucket is one period bucket (a day/week/month...). Its totals feed the
// "By date" breakdown in the menu.
struct UsageBucket: Decodable {
    let key: String
    let totals: UsageTotals
}

struct UsageReport: Decodable {
    let period: String
    let totals: UsageTotals
    let by_model: [ModelStat]
    let by_runtime: [RuntimeStat]
    let by_agent: [AgentStat]
    let buckets: [UsageBucket]

    enum CodingKeys: String, CodingKey {
        case period, totals, by_model, by_runtime, by_agent, buckets
    }

    init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        period = try container.decode(String.self, forKey: .period)
        totals = try container.decode(UsageTotals.self, forKey: .totals)
        by_model = try container.decodeIfPresent([ModelStat].self, forKey: .by_model) ?? []
        by_runtime = try container.decodeIfPresent([RuntimeStat].self, forKey: .by_runtime) ?? []
        by_agent = try container.decodeIfPresent([AgentStat].self, forKey: .by_agent) ?? []
        buckets = try container.decodeIfPresent([UsageBucket].self, forKey: .buckets) ?? []
    }
}

// MenubarSettings mirrors the daemon's /api/v1/menubar/settings JSON. Every
// field has a default so a partial or missing response still yields a usable
// configuration.
struct MenubarSettings: Decodable {
    var icon_theme: String
    var icon_stages: [String]
    var icon_metric: String
    var cost_thresholds: [Double]
    var show_messages: Bool
    var show_tokens: Bool
    var show_cost: Bool
    var show_cny: Bool
    var cny_rate: Double
    var breakdowns: [String]
    var top_n: Int

    enum CodingKeys: String, CodingKey {
        case icon_theme, icon_stages, icon_metric, cost_thresholds
        case show_messages, show_tokens, show_cost, show_cny
        case cny_rate, breakdowns, top_n
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        let d = MenubarSettings.defaults
        icon_theme = try c.decodeIfPresent(String.self, forKey: .icon_theme) ?? d.icon_theme
        icon_stages = try c.decodeIfPresent([String].self, forKey: .icon_stages) ?? d.icon_stages
        icon_metric = try c.decodeIfPresent(String.self, forKey: .icon_metric) ?? d.icon_metric
        cost_thresholds = try c.decodeIfPresent([Double].self, forKey: .cost_thresholds) ?? d.cost_thresholds
        show_messages = try c.decodeIfPresent(Bool.self, forKey: .show_messages) ?? d.show_messages
        show_tokens = try c.decodeIfPresent(Bool.self, forKey: .show_tokens) ?? d.show_tokens
        show_cost = try c.decodeIfPresent(Bool.self, forKey: .show_cost) ?? d.show_cost
        show_cny = try c.decodeIfPresent(Bool.self, forKey: .show_cny) ?? d.show_cny
        cny_rate = try c.decodeIfPresent(Double.self, forKey: .cny_rate) ?? d.cny_rate
        breakdowns = try c.decodeIfPresent([String].self, forKey: .breakdowns) ?? d.breakdowns
        top_n = try c.decodeIfPresent(Int.self, forKey: .top_n) ?? d.top_n
    }

    private init(theme: String, stages: [String], metric: String, thresholds: [Double],
                 messages: Bool, tokens: Bool, cost: Bool, cny: Bool,
                 rate: Double, breakdowns: [String], topN: Int) {
        icon_theme = theme; icon_stages = stages; icon_metric = metric
        cost_thresholds = thresholds; show_messages = messages; show_tokens = tokens
        show_cost = cost; show_cny = cny; cny_rate = rate
        self.breakdowns = breakdowns; top_n = topN
    }

    static let defaults = MenubarSettings(
        theme: "flame", stages: [], metric: "cost",
        thresholds: [0.01, 1, 10, 100],
        messages: true, tokens: true, cost: true, cny: true,
        rate: 7.2, breakdowns: ["model", "runtime", "date"], topN: 3)
}

// UsageClient fetches the daily report from the daemon.
final class UsageClient {
    func fetchDaily(completion: @escaping (UsageReport?) -> Void) {
        guard let url = URL(string: "\(daemonBase)/api/v1/usage?period=daily") else {
            DispatchQueue.main.async { completion(nil) }
            return
        }
        URLSession.shared.dataTask(with: url) { data, _, _ in
            guard let data = data,
                  let report = try? JSONDecoder().decode(UsageReport.self, from: data) else {
                DispatchQueue.main.async { completion(nil) }
                return
            }
            DispatchQueue.main.async { completion(report) }
        }.resume()
    }

    // fetchSettings loads the menubar display preferences. On any failure it
    // returns the built-in defaults so the menubar always renders.
    func fetchSettings(completion: @escaping (MenubarSettings) -> Void) {
        guard let url = URL(string: "\(daemonBase)/api/v1/menubar/settings") else {
            DispatchQueue.main.async { completion(MenubarSettings.defaults) }
            return
        }
        URLSession.shared.dataTask(with: url) { data, _, _ in
            guard let data = data,
                  let settings = try? JSONDecoder().decode(MenubarSettings.self, from: data) else {
                DispatchQueue.main.async { completion(MenubarSettings.defaults) }
                return
            }
            DispatchQueue.main.async { completion(settings) }
        }.resume()
    }
}

// Entry point. Top-level executable code is only permitted in main.swift.
let app = NSApplication.shared
let delegate = AppDelegate()
app.delegate = delegate
app.setActivationPolicy(.accessory) // menu-bar only, no dock icon
app.run()
