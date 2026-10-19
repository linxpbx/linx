import Foundation
import SwiftUI

/// The people this phone keeps at the top of Team (`docs/ui/iOS · Team &
/// presence@1x.png`, which has had a Favourites section since the mockup).
///
/// It is kept **on this phone**, by extension number: it is a convenience,
/// not a fact about the company, and nothing about it needs to reach Linx or
/// anybody else's phone. A person who loses their extension simply stops
/// appearing, because the list is only ever drawn from the team Linx sends.
///
/// A phone set up on more than one Linx keeps a star **per account**
/// (docs/MULTI_ACCOUNT.md §7): extension 101 at work and extension 101 on
/// another system are two different people, and starring one must not star
/// the other.
@MainActor @Observable final class Favourites {
    private(set) var extensions: Set<String>

    private let store: UserDefaults

    init(store: UserDefaults = .standard) {
        self.store = store
        extensions = Set(store.stringArray(forKey: Settings.favourites) ?? [])
    }

    /// A star belongs to one account's extension. An account of nil is a
    /// star from before there could be more than one account, which is what
    /// `belongTo` tidies up.
    private static func key(_ extensionNumber: String, _ account: UUID?) -> String {
        guard let account else { return extensionNumber }
        return account.uuidString.lowercased() + "/" + extensionNumber
    }

    func has(_ extensionNumber: String, on account: UUID? = nil) -> Bool {
        extensions.contains(Self.key(extensionNumber, account))
    }

    func toggle(_ extensionNumber: String, on account: UUID? = nil) {
        let key = Self.key(extensionNumber, account)
        if extensions.contains(key) {
            extensions.remove(key)
        } else {
            extensions.insert(key)
        }
        store.set(Array(extensions).sorted(), forKey: Settings.favourites)
    }

    /// The team in two parts: the starred ones first, each part in the
    /// order Linx sent (by name).
    func split(_ members: [TeamMember], on account: UUID? = nil)
        -> (favourites: [TeamMember], rest: [TeamMember])
    {
        (
            members.filter { has($0.extensionNumber, on: account) },
            members.filter { !has($0.extensionNumber, on: account) }
        )
    }

    /// belongTo gives the stars from before multi-account to the account
    /// they can only have come from — the one this phone already had. It
    /// runs once, as the list of accounts is read, and does nothing
    /// afterwards: a star that is already an account's is left alone.
    func belongTo(_ account: UUID) {
        let loose = extensions.filter { !$0.contains("/") }
        guard !loose.isEmpty else { return }
        extensions.subtract(loose)
        for extensionNumber in loose {
            extensions.insert(Self.key(extensionNumber, account))
        }
        store.set(Array(extensions).sorted(), forKey: Settings.favourites)
    }
}
