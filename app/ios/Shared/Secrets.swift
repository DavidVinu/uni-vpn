import Foundation
import Security

/// The core's API token and the key it seals secrets.json with (internal/credentials/filecred.go),
/// in the Keychain the app and the tunnel extension share: the App Group doubles as the access
/// group. Readable after the first unlock, so the VPN reconnects in a pocket; never in backups or
/// on another phone.
enum Secrets {
    static let service = "de.davidvinu.univpn"

    enum Lookup {
        case found(String)
        case missing
        case failed(OSStatus)
    }

    /// The stored value, or a new random one (32 bytes, hex) when there is none yet. `fresh` is
    /// true when it was just made, so nothing sealed before can be read with it. Nil when the
    /// Keychain cannot be read now: then nothing may be replaced.
    static func value(_ account: String) -> (hex: String, fresh: Bool)? {
        switch read(account) {
        case .found(let hex):
            return (hex, false)
        case .failed:
            return nil
        case .missing:
            guard let hex = randomHex(32) else { return nil }
            switch add(account, hex) {
            case errSecSuccess:
                return (hex, true)
            case errSecDuplicateItem:
                // The app and the extension raced: the other one's value counts.
                if case .found(let other) = read(account) { return (other, false) }
                return nil
            default:
                return nil
            }
        }
    }

    // A build without the App Group (unsigned simulator tests) keeps the items to itself.
    private static let groups: [String?] = [CoreHost.appGroup, nil]

    private static func query(_ account: String, _ group: String?) -> [String: Any] {
        var q: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
        ]
        if let group { q[kSecAttrAccessGroup as String] = group }
        return q
    }

    static func read(_ account: String) -> Lookup {
        for group in groups {
            var q = query(account, group)
            q[kSecReturnData as String] = true
            q[kSecMatchLimit as String] = kSecMatchLimitOne
            var item: CFTypeRef?
            let status = SecItemCopyMatching(q as CFDictionary, &item)
            switch status {
            case errSecSuccess:
                guard let data = item as? Data else { return .failed(status) }
                return .found(String(decoding: data, as: UTF8.self))
            case errSecItemNotFound:
                return .missing
            case errSecMissingEntitlement:
                continue
            default:
                return .failed(status)
            }
        }
        return .failed(errSecMissingEntitlement)
    }

    private static func add(_ account: String, _ value: String) -> OSStatus {
        var status = errSecMissingEntitlement
        for group in groups {
            var q = query(account, group)
            q[kSecValueData as String] = Data(value.utf8)
            q[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
            status = SecItemAdd(q as CFDictionary, nil)
            if status != errSecMissingEntitlement { return status }
        }
        return status
    }

    private static func randomHex(_ count: Int) -> String? {
        var bytes = [UInt8](repeating: 0, count: count)
        guard SecRandomCopyBytes(kSecRandomDefault, count, &bytes) == errSecSuccess else { return nil }
        return bytes.map { String(format: "%02x", $0) }.joined()
    }
}
