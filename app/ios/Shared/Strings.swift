import Foundation

/// The core's translations (/locales.json, uni_vpn/locales/<lang>.json), so phone and computer
/// say the same thing. Keys starting with "phone." replace a text that speaks of a computer.
/// The same rules as the Android app's Strings.kt.
final class Strings {
    let languages: [(code: String, name: String)]
    let lang: String
    private let catalogs: [String: [String: String]]

    init(locales: JSON?, setting: String, preferred: [String]) {
        var languages: [(code: String, name: String)] = []
        for pair in (locales?["languages"] as? [[String]]) ?? [] where pair.count == 2 {
            languages.append((pair[0], pair[1]))
        }
        self.languages = languages.isEmpty ? [("en", "English")] : languages
        catalogs = (locales?["catalogs"] as? [String: [String: String]]) ?? [:]
        if !setting.isEmpty && catalogs[setting] != nil {
            lang = setting
        } else {
            lang = Strings.systemLanguage(preferred, codes: self.languages.map(\.code))
        }
    }

    /// Like the status page: the first language of the phone that uni-vpn has, else English.
    static func systemLanguage(_ preferred: [String], codes: [String]) -> String {
        for id in preferred {
            let parts = id.replacingOccurrences(of: "_", with: "-").split(separator: "-").map(String.init)
            guard let language = parts.first?.lowercased() else { continue }
            if language == "zh" {
                let rest = parts.dropFirst()
                let script = rest.first { $0.count == 4 }
                let region = rest.first { $0.count == 2 }?.uppercased()
                let hant = script == "Hant" || (script != "Hans" && ["TW", "HK", "MO"].contains(region ?? ""))
                return hant ? "zh-Hant" : "zh-Hans"
            }
            if codes.contains(language) { return language }
        }
        return "en"
    }

    private func lookup(_ key: String) -> String? {
        if let text = catalogs[lang]?[key], !text.isEmpty { return text }
        if let text = catalogs["en"]?[key], !text.isEmpty { return text }
        return nil
    }

    func t(_ key: String, _ args: [String: Any] = [:]) -> String {
        let text = lookup("phone." + key) ?? lookup(key) ?? key
        return Strings.fill(text) { name in args[name].map(arg) }
    }

    /// A message_t or error_t object: {"key", "args"}.
    func t(_ value: JSON) -> String {
        t(value.string("key"), value.object("args")?.raw ?? [:])
    }

    /// Replaces {name} where `value` knows the name, leaves it as it is otherwise.
    static func fill(_ text: String, _ value: (String) -> String?) -> String {
        var out = ""
        var rest = Substring(text)
        while let open = rest.firstIndex(of: "{") {
            out += rest[..<open]
            let after = rest[rest.index(after: open)...]
            if let close = after.firstIndex(of: "}") {
                let name = String(after[..<close])
                let word = !name.isEmpty && name.allSatisfy { $0.isLetter || $0.isNumber || $0 == "_" }
                if word, let replacement = value(name) {
                    out += replacement
                    rest = after[after.index(after: close)...]
                    continue
                }
            }
            out += "{"
            rest = after
        }
        return out + rest
    }

    private func arg(_ value: Any) -> String {
        switch value {
        case let s as String:
            return s
        case let list as [Any]:
            return list.map(arg).joined(separator: "\n")
        case let object as [String: Any]:
            return object["key"] != nil ? t(JSON(object)) : "\(object)"
        case is NSNull:
            return ""
        default:
            return "\(value)"
        }
    }

    func plural(_ key: String, _ n: Int) -> String {
        let form = Strings.pluralForm(lang, n)
        let chosen = lookup("\(key).\(form)") != nil ? form : "other"
        return t("\(key).\(chosen)", ["n": n])
    }

    /// CLDR's cardinal rule for the languages uni-vpn has (Chinese has one form).
    static func pluralForm(_ lang: String, _ n: Int) -> String {
        switch lang {
        case "fr": return n == 0 || n == 1 ? "one" : "other"
        case "zh-Hans", "zh-Hant": return "other"
        default: return n == 1 ? "one" : "other"
        }
    }

    /// The text of the core's message, or of an API error, in the user's language.
    func message(_ text: String, _ translated: JSON?) -> String {
        if let translated, translated.has("key") { return t(translated) }
        return text
    }

    /// Another app holds the core's port, so there is no core to ask for its texts: this one
    /// comes with the app (the core's phone.msg.port_in_use, like Android's port_in_use).
    static func portTaken(_ preferred: [String]) -> String {
        let texts = [
            "en": "Another app is in the way. Uni VPN keeps trying. If this stays, restart the phone.",
            "de": "Eine andere App ist im Weg. Uni VPN versucht es weiter. Bleibt es so, starte das Handy neu.",
            "fr": "Une autre application gêne. Uni VPN continue d'essayer. Si cela persiste, redémarrez le téléphone.",
            "es": "Otra aplicación interfiere. Uni VPN sigue intentándolo. Si continúa, reinicia el teléfono.",
            "zh-Hans": "另一个应用造成了干扰。Uni VPN 会继续尝试。如果问题持续，请重启手机。",
            "zh-Hant": "另一個 App 造成了干擾。Uni VPN 會繼續嘗試。如果問題持續，請重新啟動手機。",
        ]
        return texts[systemLanguage(preferred, codes: Array(texts.keys))] ?? texts["en", default: ""]
    }
}
