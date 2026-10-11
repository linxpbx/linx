import Foundation
import MetricKit

// A crash of the app, sent to Linx the next time it runs (owner, 2026-10-11).
//
// iOS writes a report of every crash and hands it to the app later, on its
// next launch or within a day (Apple's MetricKit), with the crashed thread's
// frames unsymbolicated: a binary and an offset, which the archive's dSYM
// turns back into a line of code. The app sends each one once, as a record
// of kind "crash" beside the phone's call records, so a crash can be
// understood without a cable to the phone. Nothing else MetricKit collects
// (battery, hangs, disk) is read.

@MainActor final class CrashReports: NSObject {
    static let shared = CrashReports()

    /// Sends one report to Linx, answering whether it got there. Set once
    /// the phone is signed in; until then reports wait.
    var send: ((CallReport) async -> Bool)? {
        didSet { Task { await sendWhatIsWaiting() } }
    }

    private var waiting: [CallReport] = []
    private var sending = false
    private static let sentKey = "linx.crashReportsSent"
    private static let rememberSent = 50

    /// Listens for MetricKit's deliveries, and picks up any it made before.
    func start() {
        MXMetricManager.shared.add(self)
        waiting += Self.reports(from: MXMetricManager.shared.pastDiagnosticPayloads)
    }

    private func sendWhatIsWaiting() async {
        guard let send, !sending else { return }
        sending = true
        defer { sending = false }
        var sent = UserDefaults.standard.stringArray(forKey: Self.sentKey) ?? []
        for report in waiting where !sent.contains(report.callID) {
            if await send(report) {
                sent.append(report.callID)
            }
        }
        waiting.removeAll { sent.contains($0.callID) }
        UserDefaults.standard.set(Array(sent.suffix(Self.rememberSent)), forKey: Self.sentKey)
    }

    nonisolated static func reports(from payloads: [MXDiagnosticPayload]) -> [CallReport] {
        payloads.flatMap { payload in
            (payload.crashDiagnostics ?? []).map { crash in
                report(
                    at: payload.timeStampEnd, app: crash.metaData.applicationBuildVersion,
                    os: crash.metaData.osVersion, exception: crash.exceptionType?.intValue,
                    code: crash.exceptionCode?.intValue, signal: crash.signal?.intValue,
                    termination: crash.terminationReason, stack: crash.callStackTree.jsonRepresentation())
            }
        }
    }

    /// One crash in words. The id is the crash's time and its first frame,
    /// so the same crash is never sent twice however often iOS hands it over.
    nonisolated static func report(
        at: Date, app: String, os: String, exception: Int?, code: Int?, signal: Int?, termination: String?,
        stack: Data
    ) -> CallReport {
        let frames = crashedThread(in: stack)
        var lines = ["Linx crashed", "build: \(app), \(os)"]
        if let exception {
            let name = exceptions[exception].map { "\($0) (\(exception))" } ?? "\(exception)"
            lines.append("exception: \(name)\(code.map { ", code \($0)" } ?? "")")
        }
        if let signal { lines.append("signal: \(signals[signal].map { "\($0) (\(signal))" } ?? "\(signal)")") }
        if let termination, !termination.isEmpty { lines.append("termination: \(termination)") }
        lines.append("crashed thread:")
        lines += frames.map { "  \($0)" }
        let id = "crash-\(Int(at.timeIntervalSince1970))-\(frames.first ?? "")".prefix(200)
        return CallReport(
            callID: String(id), incoming: false, peer: "", startedAt: at, endedAt: nil,
            text: String(lines.joined(separator: "\n").prefix(16_000)), kind: .crash)
    }

    nonisolated private static let exceptions = [
        1: "EXC_BAD_ACCESS", 2: "EXC_BAD_INSTRUCTION", 3: "EXC_ARITHMETIC", 6: "EXC_BREAKPOINT", 10: "EXC_CRASH",
        11: "EXC_RESOURCE", 12: "EXC_GUARD",
    ]
    nonisolated private static let signals = [
        4: "SIGILL", 5: "SIGTRAP", 6: "SIGABRT", 7: "SIGEMT", 8: "SIGFPE", 9: "SIGKILL", 10: "SIGBUS", 11: "SIGSEGV",
    ]

    /// The crashed thread's frames, innermost first: "Linx +0x16a88
    /// (BAE0B802)", the binary's UUID matching the archive's dSYM.
    nonisolated static func crashedThread(in json: Data) -> [String] {
        guard let tree = try? JSONSerialization.jsonObject(with: json) as? [String: Any],
            let stacks = tree["callStacks"] as? [[String: Any]]
        else { return [] }
        let stack = stacks.first { $0["threadAttributed"] as? Bool == true } ?? stacks.first
        var out: [String] = []
        var frame = (stack?["callStackRootFrames"] as? [[String: Any]])?.first
        while let current = frame, out.count < 60 {
            let binary = current["binaryName"] as? String ?? "?"
            let offset = (current["offsetIntoBinaryTextSegment"] as? NSNumber)?.intValue ?? 0
            let uuid = (current["binaryUUID"] as? String).map { " (\($0.prefix(8)))" } ?? ""
            out.append("\(binary) +0x\(String(offset, radix: 16))\(uuid)")
            frame = (current["subFrames"] as? [[String: Any]])?.first
        }
        return out
    }
}

extension CrashReports: MXMetricManagerSubscriber {
    nonisolated func didReceive(_ payloads: [MXDiagnosticPayload]) {
        let reports = Self.reports(from: payloads)
        Task { @MainActor in
            self.waiting += reports
            await self.sendWhatIsWaiting()
        }
    }
}
