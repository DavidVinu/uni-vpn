import Foundation
#if canImport(FoundationNetworking)
import FoundationNetworking
#endif

/// A JSON object of the core, read like Android's JSONObject: a missing value reads as empty.
struct JSON {
    let raw: [String: Any]

    init(_ raw: [String: Any]) { self.raw = raw }

    init?(data: Data) {
        guard let object = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else { return nil }
        raw = object
    }

    subscript(key: String) -> Any? {
        let value = raw[key]
        return value is NSNull ? nil : value
    }

    func has(_ key: String) -> Bool { self[key] != nil }

    func string(_ key: String) -> String {
        switch self[key] {
        case let s as String: return s
        case let n as NSNumber: return n.stringValue
        default: return ""
        }
    }

    func bool(_ key: String) -> Bool { (self[key] as? NSNumber)?.boolValue ?? false }

    func int(_ key: String, _ fallback: Int = 0) -> Int { (self[key] as? NSNumber)?.intValue ?? fallback }

    func object(_ key: String) -> JSON? { (self[key] as? [String: Any]).map(JSON.init) }

    func strings(_ key: String) -> [String] { (self[key] as? [Any])?.compactMap { $0 as? String } ?? [] }

    func objects(_ key: String) -> [JSON] { (self[key] as? [Any])?.compactMap { ($0 as? [String: Any]).map(JSON.init) } ?? [] }
}

/// One answer of the core; status 0 when it is not reachable.
struct Reply {
    let status: Int
    let json: JSON?
    let text: String
    var ok: Bool { (200..<300).contains(status) }
}

/// The core's local API on 127.0.0.1:1081, like Android's Core.call: every request carries the
/// token, as every app on the phone can reach the port.
enum API {
    static let base = "http://127.0.0.1:\(CoreHost.port)"

    private static let session: URLSession = {
        let config = URLSessionConfiguration.ephemeral
        // Straight to the core, whatever proxy rule the VPN hands the phone.
        config.connectionProxyDictionary = [:]
        config.requestCachePolicy = .reloadIgnoringLocalCacheData
        config.timeoutIntervalForResource = 120
        return URLSession(configuration: config)
    }()

    static func call(_ method: String, _ path: String, body: [String: Any]? = nil, timeout: TimeInterval = 10,
                     token: Bool = true) async -> Reply {
        guard CoreHost.ready else { return Reply(status: 0, json: nil, text: "the core is not ready") }
        guard let url = URL(string: base + path) else { return Reply(status: 0, json: nil, text: "bad path") }
        var request = URLRequest(url: url, timeoutInterval: timeout)
        request.httpMethod = method
        request.setValue("1", forHTTPHeaderField: "X-Uni-VPN")
        if token, let value = CoreHost.token() {
            request.setValue(value, forHTTPHeaderField: "X-Uni-VPN-Token")
        }
        if method == "POST" {
            request.setValue("application/json", forHTTPHeaderField: "Content-Type")
            request.httpBody = (try? JSONSerialization.data(withJSONObject: body ?? [:])) ?? Data("{}".utf8)
        }
        do {
            let (data, response) = try await session.data(for: request)
            let status = (response as? HTTPURLResponse)?.statusCode ?? 0
            return Reply(status: status, json: JSON(data: data), text: String(decoding: data, as: UTF8.self))
        } catch {
            return Reply(status: 0, json: nil, text: error.localizedDescription)
        }
    }

    static func get(_ path: String) async -> Reply { await call("GET", path) }

    static func post(_ path: String, _ body: [String: Any]? = nil, timeout: TimeInterval = 10) async -> Reply {
        await call("POST", path, body: body, timeout: timeout)
    }

    /// The proxy rule's address; the version changes with the rule, so browsers load the new
    /// one.
    static func pacURL(version: String) -> URL? { URL(string: "\(base)/proxy.pac?v=\(version)") }
}
