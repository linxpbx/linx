import Foundation

/// Call reports that couldn't be sent when their call ended, kept on this
/// phone and sent the next time its line opens.
///
/// A call that goes wrong is often one whose network has gone: the owner's
/// iPhone lost its line at the end of a bad call, and the report that would
/// have said what happened went with it (2026-10-16). At most `most` are
/// kept, newest first, and none older than a week — the server keeps them
/// for no longer than that.
enum UnsentReports {
    static let most = 10
    static let keptFor: TimeInterval = 7 * 24 * 3600

    private static var file: URL {
        URL.applicationSupportDirectory.appending(path: "unsent-call-reports.json")
    }

    static func keep(_ report: CallReport, now: Date = Date()) {
        var all = load(now: now)
        all.removeAll { $0.callID == report.callID && $0.kind == report.kind }
        all.insert(report, at: 0)
        save(Array(all.prefix(most)))
    }

    /// Everything waiting, and none of it kept any longer: what isn't sent
    /// again is kept again by whoever sends it.
    static func takeAll(now: Date = Date()) -> [CallReport] {
        let all = load(now: now)
        if !all.isEmpty { save([]) }
        return all
    }

    private static func load(now: Date) -> [CallReport] {
        guard let data = try? Data(contentsOf: file),
            let all = try? JSONDecoder().decode([CallReport].self, from: data)
        else { return [] }
        return all.filter { now.timeIntervalSince($0.startedAt) < keptFor }
    }

    private static func save(_ all: [CallReport]) {
        guard !all.isEmpty else {
            try? FileManager.default.removeItem(at: file)
            return
        }
        try? FileManager.default.createDirectory(
            at: URL.applicationSupportDirectory, withIntermediateDirectories: true)
        try? JSONEncoder().encode(all).write(to: file, options: [.atomic, .completeFileProtection])
    }
}
