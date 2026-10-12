import Foundation
#if canImport(DeveloperToolsSupport)
import DeveloperToolsSupport
#endif

#if SWIFT_PACKAGE
private let resourceBundle = Foundation.Bundle.module
#else
private class ResourceBundleClass {}
private let resourceBundle = Foundation.Bundle(for: ResourceBundleClass.self)
#endif

// MARK: - Color Symbols -

@available(iOS 17.0, macOS 14.0, tvOS 17.0, watchOS 10.0, *)
extension DeveloperToolsSupport.ColorResource {

    /// The "Accent" asset catalog color resource.
    static let accent = DeveloperToolsSupport.ColorResource(name: "Accent", bundle: resourceBundle)

    /// The "AccentOnDark" asset catalog color resource.
    static let accentOnDark = DeveloperToolsSupport.ColorResource(name: "AccentOnDark", bundle: resourceBundle)

    /// The "Bg" asset catalog color resource.
    static let bg = DeveloperToolsSupport.ColorResource(name: "Bg", bundle: resourceBundle)

    /// The "Border" asset catalog color resource.
    static let border = DeveloperToolsSupport.ColorResource(name: "Border", bundle: resourceBundle)

    /// The "BrandFill" asset catalog color resource.
    static let brandFill = DeveloperToolsSupport.ColorResource(name: "BrandFill", bundle: resourceBundle)

    /// The "Call" asset catalog color resource.
    static let call = DeveloperToolsSupport.ColorResource(name: "Call", bundle: resourceBundle)

    /// The "End" asset catalog color resource.
    static let end = DeveloperToolsSupport.ColorResource(name: "End", bundle: resourceBundle)

    /// The "OnBrand" asset catalog color resource.
    static let onBrand = DeveloperToolsSupport.ColorResource(name: "OnBrand", bundle: resourceBundle)

    /// The "OnCall" asset catalog color resource.
    static let onCall = DeveloperToolsSupport.ColorResource(name: "OnCall", bundle: resourceBundle)

    /// The "OnEnd" asset catalog color resource.
    static let onEnd = DeveloperToolsSupport.ColorResource(name: "OnEnd", bundle: resourceBundle)

    /// The "OnStar" asset catalog color resource.
    static let onStar = DeveloperToolsSupport.ColorResource(name: "OnStar", bundle: resourceBundle)

    /// The "OnSurfaceDark" asset catalog color resource.
    static let onSurfaceDark = DeveloperToolsSupport.ColorResource(name: "OnSurfaceDark", bundle: resourceBundle)

    /// The "Star" asset catalog color resource.
    static let star = DeveloperToolsSupport.ColorResource(name: "Star", bundle: resourceBundle)

    /// The "StatusAvailable" asset catalog color resource.
    static let statusAvailable = DeveloperToolsSupport.ColorResource(name: "StatusAvailable", bundle: resourceBundle)

    /// The "StatusAway" asset catalog color resource.
    static let statusAway = DeveloperToolsSupport.ColorResource(name: "StatusAway", bundle: resourceBundle)

    /// The "StatusBusy" asset catalog color resource.
    static let statusBusy = DeveloperToolsSupport.ColorResource(name: "StatusBusy", bundle: resourceBundle)

    /// The "StatusDnd" asset catalog color resource.
    static let statusDnd = DeveloperToolsSupport.ColorResource(name: "StatusDnd", bundle: resourceBundle)

    /// The "StatusMeeting" asset catalog color resource.
    static let statusMeeting = DeveloperToolsSupport.ColorResource(name: "StatusMeeting", bundle: resourceBundle)

    /// The "StatusOffline" asset catalog color resource.
    static let statusOffline = DeveloperToolsSupport.ColorResource(name: "StatusOffline", bundle: resourceBundle)

    /// The "StatusPush" asset catalog color resource.
    static let statusPush = DeveloperToolsSupport.ColorResource(name: "StatusPush", bundle: resourceBundle)

    /// The "StatusRinging" asset catalog color resource.
    static let statusRinging = DeveloperToolsSupport.ColorResource(name: "StatusRinging", bundle: resourceBundle)

    /// The "Surface" asset catalog color resource.
    static let surface = DeveloperToolsSupport.ColorResource(name: "Surface", bundle: resourceBundle)

    /// The "SurfaceDark" asset catalog color resource.
    static let surfaceDark = DeveloperToolsSupport.ColorResource(name: "SurfaceDark", bundle: resourceBundle)

    /// The "Text" asset catalog color resource.
    static let text = DeveloperToolsSupport.ColorResource(name: "Text", bundle: resourceBundle)

    /// The "TextMuted" asset catalog color resource.
    static let textMuted = DeveloperToolsSupport.ColorResource(name: "TextMuted", bundle: resourceBundle)

}

// MARK: - Image Symbols -

@available(iOS 17.0, macOS 14.0, tvOS 17.0, watchOS 10.0, *)
extension DeveloperToolsSupport.ImageResource {

}

