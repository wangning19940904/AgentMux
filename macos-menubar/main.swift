// AgentMux macOS menu bar app.
//
// A lightweight SwiftUI/AppKit status-bar item that shows the AgentMux logo
// and polls the local daemon (http://127.0.0.1:8765/api/v1/usage) for a
// dropdown breakdown of recent usage. Inspired by
// CodeBurn / cc-statistics menu bar panels, but it reads from our own daemon so
// there is a single source of truth across CLI, WebUI and desktop.
//
// Build (macOS, requires Xcode command line tools):
//   swiftc -O -o AgentMuxMenuBar main.swift -framework AppKit -framework SwiftUI
// Then run ./AgentMuxMenuBar (the daemon must be running: `amux serve`).

import AppKit
import SwiftUI

let daemonBase = ProcessInfo.processInfo.environment["AMUX_ADDR"] ?? "http://127.0.0.1:8765"

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

// UsageClient fetches usage reports from the daemon.
final class UsageClient {
    // fetchDays loads the report for the last `days` local days, today
    // included, bucketed by day. The menu shows today's figures; asking
    // without a range made the daemon total its whole history every minute
    // and labelled that total "Today".
    func fetchDays(_ days: Int, completion: @escaping (UsageReport?) -> Void) {
        let calendar = Calendar.current
        let today = calendar.startOfDay(for: Date())
        let start = calendar.date(byAdding: .day, value: -(max(days, 1) - 1), to: today) ?? today
        let format = DateFormatter()
        format.calendar = Calendar(identifier: .gregorian)
        format.locale = Locale(identifier: "en_US_POSIX")
        format.timeZone = calendar.timeZone
        format.dateFormat = "yyyy-MM-dd"
        var components = URLComponents(string: "\(daemonBase)/api/v1/usage")
        components?.queryItems = [
            URLQueryItem(name: "period", value: "daily"),
            URLQueryItem(name: "from", value: format.string(from: start)),
            URLQueryItem(name: "to", value: format.string(from: today)),
            URLQueryItem(name: "timezone", value: calendar.timeZone.identifier),
        ]
        guard let url = components?.url else {
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
}

// Entry point. Top-level executable code is only permitted in main.swift.
let app = NSApplication.shared
let delegate = AppDelegate()
app.delegate = delegate
app.setActivationPolicy(.accessory) // menu-bar only, no dock icon
app.run()
