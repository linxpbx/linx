import Network
import Synchronization

/// Whether this phone is on mobile data right now, read from iOS's own
/// view of the network and kept up to date as it changes.
///
/// A phone on mobile data sends its picture through Linx's relay and
/// nowhere else (`WebRTCMedia.startPicture`). On the owner's 5G the direct
/// route between the two devices answered WebRTC's checks while the picture
/// itself stopped for seconds at a time, both ways; WebRTC kept trading it
/// for the relay and back, eight times in a minute and a half, and each
/// trade was another freeze — while the sound, which always goes through
/// the relay, carried on (2026-10-15).
enum MobileData {
    private static let state = Mutex(false)
    private static let monitor = NWPathMonitor()

    /// Starts watching; once is enough, and it costs nothing between changes.
    static func watch() {
        monitor.pathUpdateHandler = { path in
            let mobile =
                path.status == .satisfied && path.usesInterfaceType(.cellular)
                && !path.usesInterfaceType(.wifi) && !path.usesInterfaceType(.wiredEthernet)
            state.withLock { $0 = mobile }
        }
        monitor.start(queue: DispatchQueue(label: "linx.mobile-data", qos: .utility))
    }

    /// Whether the phone is on mobile data now (the bench can say so too).
    static var now: Bool { Bench.mobileData || state.withLock { $0 } }
}
