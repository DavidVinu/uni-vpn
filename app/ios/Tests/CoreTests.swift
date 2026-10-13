import XCTest
@testable import UniVPN

/// What the simulator can answer: the core and libopenconnect run inside the app, only the app
/// may use the API, the proxy rule is open to browsers, and a made-up account reaches a real
/// gateway and is turned down, with the secrets sealed. The tunnel extension cannot run in the
/// simulator; docs/ios.md says what only a phone proves.
final class CoreTests: XCTestCase {
    @MainActor
    private func startedCore() async throws {
        // The test host's own model may have started it already; Start says so then.
        let result = await CoreHost.shared.start(lockWaitMs: 10_000)
        XCTAssertEqual(result, .started)
        for _ in 0..<100 {
            if await API.get("/status.json").ok { return }
            try await Task.sleep(nanoseconds: 200_000_000)
        }
        XCTFail("the core did not answer")
    }

    private func plainStatus(_ path: String) async throws -> Int {
        var request = URLRequest(url: URL(string: API.base + path)!)
        request.setValue("1", forHTTPHeaderField: "X-Uni-VPN")
        let (_, response) = try await URLSession(configuration: .ephemeral).data(for: request)
        return (response as? HTTPURLResponse)?.statusCode ?? 0
    }

    @MainActor
    func testOnlyTheAppMayUseTheAPI() async throws {
        try await startedCore()
        let withoutToken = try await plainStatus("/status.json")
        XCTAssertEqual(withoutToken, 403)
        let pac = try await plainStatus("/proxy.pac")
        XCTAssertEqual(pac, 200)
        let locales = await API.get("/locales.json")
        XCTAssertTrue(locales.ok)
        XCTAssertNotNil(locales.json?.object("catalogs")?.object("de"))
        // The core says it holds its ports; the text the app shows when it does not is the
        // core's own.
        XCTAssertTrue(CoreHost.ready)
        for code in ["en", "de", "fr", "es", "zh-Hans", "zh-Hant"] {
            let core = locales.json?.object("catalogs")?.object(code)?.string("phone.msg.port_in_use")
            XCTAssertEqual(Strings.portTaken([code]), core, code)
        }
    }

    @MainActor
    func testAMadeUpAccountIsTurnedDownAndSecretsStaySealed() async throws {
        try await startedCore()
        let password = "wrong-password"
        let setup = await API.post("/api/setup", [
            "user": "ab123",
            "password": password,
            "secret": "JBSWY3DPEHPK3PXP",
            "university": "heidelberg",
        ], timeout: 60)
        XCTAssertTrue(setup.ok, setup.text)
        // Getting a "no" means openconnect ran in this process, resolved the gateway, trusted its
        // certificate through iOS and spoke AnyConnect.
        var state = ""
        var last: JSON?
        for _ in 0..<240 {
            last = await API.get("/status.json").json
            state = last?.string("state") ?? ""
            if state == "auth_failed" || state == "connected" { break }
            if state == "idle" && last?.bool("busy") == false {
                _ = await API.post("/api/connect")
            }
            try await Task.sleep(nanoseconds: 500_000_000)
        }
        XCTAssertEqual(state, "auth_failed", "\(last?.raw ?? [:])")
        XCTAssertEqual(last?.bool("setup_needed"), false)
        let secrets = try String(contentsOf: CoreHost.secretsFile, encoding: .utf8)
        XCTAssertFalse(secrets.contains(password))
        XCTAssertFalse(secrets.contains("JBSWY3DP"))
    }

    func testStringsFollowTheAndroidRules() {
        let locales = JSON([
            "languages": [["en", "English"], ["de", "Deutsch"], ["zh-Hant", "繁體中文"]],
            "catalogs": [
                "en": ["a.b": "Step {n} of {total}", "phone.c": "phone", "c": "computer",
                       "settings.domain_count.one": "{n} website", "settings.domain_count.other": "{n} websites"],
                "de": ["a.b": "Schritt {n} von {total}"],
            ],
        ])
        let de = Strings(locales: locales, setting: "", preferred: ["de-DE", "en-US"])
        XCTAssertEqual(de.lang, "de")
        XCTAssertEqual(de.t("a.b", ["n": 1, "total": 4]), "Schritt 1 von 4")
        XCTAssertEqual(de.t("c"), "phone")
        XCTAssertEqual(de.t("missing.key"), "missing.key")
        let en = Strings(locales: locales, setting: "en", preferred: ["de-DE"])
        XCTAssertEqual(en.plural("settings.domain_count", 1), "1 website")
        XCTAssertEqual(en.plural("settings.domain_count", 3), "3 websites")
        XCTAssertEqual(Strings.systemLanguage(["zh-HK"], codes: ["en"]), "zh-Hant")
        XCTAssertEqual(Strings.systemLanguage(["zh-Hans-CN"], codes: ["en"]), "zh-Hans")
        XCTAssertEqual(Strings.systemLanguage(["pt-BR"], codes: ["en", "de"]), "en")
        XCTAssertEqual(Strings.fill("{a} {b} {}", { $0 == "a" ? "x" : nil }), "x {b} {}")
    }
}
