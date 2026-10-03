// Generated from design/tokens.json by `make tokens`. Do not edit.

import SwiftUI

/// Colours from the Colors asset catalog, which `make tokens` generates from the same file.
public enum LinxColor {
    /// links, selected nav, focus rings, accent text
    public static let accent = Color("Accent", bundle: .main)
    /// Signal: logo X, links and accents on surface-dark
    public static let accentOnDark = Color("AccentOnDark", bundle: .main)
    /// app background
    public static let bg = Color("Bg", bundle: .main)
    /// dividers, outlines
    public static let border = Color("Border", bundle: .main)
    /// filled primary buttons
    public static let brandFill = Color("BrandFill", bundle: .main)
    /// answer button, available
    public static let call = Color("Call", bundle: .main)
    /// hang up, destructive, alerts
    public static let end = Color("End", bundle: .main)
    /// text/icons on brand-fill
    public static let onBrand = Color("OnBrand", bundle: .main)
    /// icon on call
    public static let onCall = Color("OnCall", bundle: .main)
    /// text/icon on end
    public static let onEnd = Color("OnEnd", bundle: .main)
    /// text on surface-dark
    public static let onSurfaceDark = Color("OnSurfaceDark", bundle: .main)
    /// cards, tables, panels
    public static let surface = Color("Surface", bundle: .main)
    /// sidebar, in-call screen, meeting stage
    public static let surfaceDark = Color("SurfaceDark", bundle: .main)
    /// body text
    public static let text = Color("Text", bundle: .main)
    /// secondary text
    public static let textMuted = Color("TextMuted", bundle: .main)

    /// Presence colours. Never shown without a text label.
    public enum Status {
        public static let available = Color("StatusAvailable", bundle: .main)
        public static let away = Color("StatusAway", bundle: .main)
        public static let busy = Color("StatusBusy", bundle: .main)
        public static let dnd = Color("StatusDnd", bundle: .main)
        public static let meeting = Color("StatusMeeting", bundle: .main)
        public static let offline = Color("StatusOffline", bundle: .main)
        public static let push = Color("StatusPush", bundle: .main)
        public static let ringing = Color("StatusRinging", bundle: .main)
    }
}

/// The spacing scale, in points.
public enum LinxSpace {
    public static let s1: CGFloat = 4
    public static let s2: CGFloat = 8
    public static let s3: CGFloat = 12
    public static let s4: CGFloat = 16
    public static let s5: CGFloat = 20
    public static let s6: CGFloat = 24
    public static let s8: CGFloat = 32
    public static let s10: CGFloat = 40
}

/// Corner radii, in points.
public enum LinxRadius {
    public static let full: CGFloat = 9999
    public static let lg: CGFloat = 16
    public static let md: CGFloat = 12
    public static let sm: CGFloat = 8
}

#if DEBUG
    /// The same colours as plain hex, so the tests can check the asset catalog against
    /// design/tokens.json. Debug builds only, so the shipped app carries none of it.
    public enum LinxTokenReference {
        public static let colors: [String: (light: String, dark: String)] = [
            "Accent": ("#1F5FD6", "#7FB0FF"),
            "AccentOnDark": ("#7FB0FF", "#7FB0FF"),
            "Bg": ("#F4F3EF", "#17191E"),
            "Border": ("#E3E1DA", "#343842"),
            "BrandFill": ("#1F5FD6", "#1F5FD6"),
            "Call": ("#1E8E4E", "#1E8E4E"),
            "End": ("#C53030", "#C53030"),
            "OnBrand": ("#FFFFFF", "#FFFFFF"),
            "OnCall": ("#FFFFFF", "#FFFFFF"),
            "OnEnd": ("#FFFFFF", "#FFFFFF"),
            "OnSurfaceDark": ("#F4F3EF", "#F4F3EF"),
            "Surface": ("#FFFFFF", "#22252C"),
            "SurfaceDark": ("#17191E", "#17191E"),
            "Text": ("#17191E", "#F4F3EF"),
            "TextMuted": ("#5B5F68", "#A6AAB3"),
            "StatusAvailable": ("#1E8E4E", "#1E8E4E"),
            "StatusAway": ("#9C7F0A", "#B8960C"),
            "StatusBusy": ("#C53030", "#E05252"),
            "StatusDnd": ("#7A3FC4", "#9D6FE3"),
            "StatusMeeting": ("#1F5FD6", "#5B8FEA"),
            "StatusOffline": ("#8A8E96", "#8A8E96"),
            "StatusPush": ("#5A7A9A", "#5A7A9A"),
            "StatusRinging": ("#C27A12", "#C27A12"),
        ]
    }
#endif
