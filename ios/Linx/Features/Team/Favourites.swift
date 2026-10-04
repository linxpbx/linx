import Foundation
import SwiftUI

/// The people this phone keeps at the top of Team (`docs/ui/iOS · Team &
/// presence@1x.png`, which has had a Favourites section since the mockup).
///
/// It is kept **on this phone**, by extension number: it is a convenience,
/// not a fact about the company, and nothing about it needs to reach Linx or
/// anybody else's phone. A person who loses their extension simply stops
/// appearing, because the list is only ever drawn from the team Linx sends.
@MainActor @Observable final class Favourites {
    private(set) var extensions: Set<String>

    private let store: UserDefaults

    init(store: UserDefaults = .standard) {
        self.store = store
        extensions = Set(store.stringArray(forKey: Settings.favourites) ?? [])
    }

    func has(_ extensionNumber: String) -> Bool { extensions.contains(extensionNumber) }

    func toggle(_ extensionNumber: String) {
        if extensions.contains(extensionNumber) {
            extensions.remove(extensionNumber)
        } else {
            extensions.insert(extensionNumber)
        }
        store.set(Array(extensions).sorted(), forKey: Settings.favourites)
    }

    /// The team in two parts: the starred ones first, each part in the
    /// order Linx sent (by name).
    func split(_ members: [TeamMember]) -> (favourites: [TeamMember], rest: [TeamMember]) {
        (members.filter { has($0.extensionNumber) }, members.filter { !has($0.extensionNumber) })
    }
}
