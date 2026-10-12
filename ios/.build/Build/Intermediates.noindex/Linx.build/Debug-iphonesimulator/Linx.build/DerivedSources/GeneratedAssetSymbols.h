#import <Foundation/Foundation.h>

#if __has_attribute(swift_private)
#define AC_SWIFT_PRIVATE __attribute__((swift_private))
#else
#define AC_SWIFT_PRIVATE
#endif

/// The resource bundle ID.
static NSString * const ACBundleID AC_SWIFT_PRIVATE = @"com.linxpbx.app";

/// The "Accent" asset catalog color resource.
static NSString * const ACColorNameAccent AC_SWIFT_PRIVATE = @"Accent";

/// The "AccentOnDark" asset catalog color resource.
static NSString * const ACColorNameAccentOnDark AC_SWIFT_PRIVATE = @"AccentOnDark";

/// The "Bg" asset catalog color resource.
static NSString * const ACColorNameBg AC_SWIFT_PRIVATE = @"Bg";

/// The "Border" asset catalog color resource.
static NSString * const ACColorNameBorder AC_SWIFT_PRIVATE = @"Border";

/// The "BrandFill" asset catalog color resource.
static NSString * const ACColorNameBrandFill AC_SWIFT_PRIVATE = @"BrandFill";

/// The "Call" asset catalog color resource.
static NSString * const ACColorNameCall AC_SWIFT_PRIVATE = @"Call";

/// The "End" asset catalog color resource.
static NSString * const ACColorNameEnd AC_SWIFT_PRIVATE = @"End";

/// The "OnBrand" asset catalog color resource.
static NSString * const ACColorNameOnBrand AC_SWIFT_PRIVATE = @"OnBrand";

/// The "OnCall" asset catalog color resource.
static NSString * const ACColorNameOnCall AC_SWIFT_PRIVATE = @"OnCall";

/// The "OnEnd" asset catalog color resource.
static NSString * const ACColorNameOnEnd AC_SWIFT_PRIVATE = @"OnEnd";

/// The "OnStar" asset catalog color resource.
static NSString * const ACColorNameOnStar AC_SWIFT_PRIVATE = @"OnStar";

/// The "OnSurfaceDark" asset catalog color resource.
static NSString * const ACColorNameOnSurfaceDark AC_SWIFT_PRIVATE = @"OnSurfaceDark";

/// The "Star" asset catalog color resource.
static NSString * const ACColorNameStar AC_SWIFT_PRIVATE = @"Star";

/// The "StatusAvailable" asset catalog color resource.
static NSString * const ACColorNameStatusAvailable AC_SWIFT_PRIVATE = @"StatusAvailable";

/// The "StatusAway" asset catalog color resource.
static NSString * const ACColorNameStatusAway AC_SWIFT_PRIVATE = @"StatusAway";

/// The "StatusBusy" asset catalog color resource.
static NSString * const ACColorNameStatusBusy AC_SWIFT_PRIVATE = @"StatusBusy";

/// The "StatusDnd" asset catalog color resource.
static NSString * const ACColorNameStatusDnd AC_SWIFT_PRIVATE = @"StatusDnd";

/// The "StatusMeeting" asset catalog color resource.
static NSString * const ACColorNameStatusMeeting AC_SWIFT_PRIVATE = @"StatusMeeting";

/// The "StatusOffline" asset catalog color resource.
static NSString * const ACColorNameStatusOffline AC_SWIFT_PRIVATE = @"StatusOffline";

/// The "StatusPush" asset catalog color resource.
static NSString * const ACColorNameStatusPush AC_SWIFT_PRIVATE = @"StatusPush";

/// The "StatusRinging" asset catalog color resource.
static NSString * const ACColorNameStatusRinging AC_SWIFT_PRIVATE = @"StatusRinging";

/// The "Surface" asset catalog color resource.
static NSString * const ACColorNameSurface AC_SWIFT_PRIVATE = @"Surface";

/// The "SurfaceDark" asset catalog color resource.
static NSString * const ACColorNameSurfaceDark AC_SWIFT_PRIVATE = @"SurfaceDark";

/// The "Text" asset catalog color resource.
static NSString * const ACColorNameText AC_SWIFT_PRIVATE = @"Text";

/// The "TextMuted" asset catalog color resource.
static NSString * const ACColorNameTextMuted AC_SWIFT_PRIVATE = @"TextMuted";

#undef AC_SWIFT_PRIVATE
