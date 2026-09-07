import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import vm from "node:vm";

const source = readFileSync(new URL("../../extension/common.js", import.meta.url), "utf8");
// Als Skript laden (kein Modul, wie im Browser), im selben Realm wie die Assertions:
// Arrays aus einem fremden vm-Kontext scheitern an deepEqual (Prototyp-Vergleich).
vm.runInThisContext(source);
const U = globalThis.UniVpn;

test("Defaults enthalten die beiden Uni-Domains", () => {
  assert.deepEqual(U.DEFAULTS.domains, ["sogo.uni-heidelberg.de", "elearning-med.uni-heidelberg.de"]);
  assert.equal(U.DEFAULTS.socksPort, 1080);
  assert.equal(U.DEFAULTS.httpPort, 1081);
  assert.equal(U.DEFAULTS.enabled, true);
});

test("parseDomainList normalisiert und meldet Fehler mit Zeile", () => {
  const r = U.parseDomainList("Sogo.Uni-Heidelberg.DE\n# Kommentar\n\n*.example.org  # trailing\nhttps://bad.example/x\nlocalhost\nsogo.uni-heidelberg.de\n");
  assert.deepEqual(r.domains, ["sogo.uni-heidelberg.de", "example.org"]);
  assert.equal(r.errors.length, 2);
  assert.match(r.errors[0], /Zeile 5/);
  assert.match(r.errors[1], /Zeile 6/);
});

test("matches trifft Host und Subdomains, nicht Suffix-Verwandte", () => {
  const d = ["uni-heidelberg.de"];
  assert.equal(U.matches("uni-heidelberg.de", d), true);
  assert.equal(U.matches("sogo.uni-heidelberg.de", d), true);
  assert.equal(U.matches("SOGO.uni-heidelberg.de.", d), true);
  assert.equal(U.matches("notuni-heidelberg.de", d), false);
  assert.equal(U.matches("uni-heidelberg.de.evil.example", d), false);
  assert.equal(U.matches("", d), false);
});

test("buildPac liefert SOCKS5 nur fuer gelistete Hosts, ohne DIRECT-Fallback", () => {
  const pac = U.buildPac(["sogo.uni-heidelberg.de", "example.org"], 1080);
  const c = vm.createContext({});
  vm.runInContext(pac, c);
  const f = c.FindProxyForURL;
  assert.equal(f("https://sogo.uni-heidelberg.de/SOGo/", "sogo.uni-heidelberg.de"), "SOCKS5 127.0.0.1:1080");
  assert.equal(f("https://a.b.example.org/", "a.b.example.org"), "SOCKS5 127.0.0.1:1080");
  assert.equal(f("https://www.uni-heidelberg.de/", "www.uni-heidelberg.de"), "DIRECT");
  assert.equal(f("https://notexample.org/", "notexample.org"), "DIRECT");
  assert.equal(f("https://x/", "SOGO.UNI-HEIDELBERG.DE"), "SOCKS5 127.0.0.1:1080");
  assert.ok(!pac.includes("SOCKS5 127.0.0.1:1080; DIRECT"));
  assert.ok(!pac.includes('"SOCKS '));
});

test("buildPac mit anderem Port und leerer Liste", () => {
  const c = vm.createContext({});
  vm.runInContext(U.buildPac([], 10800), c);
  assert.equal(c.FindProxyForURL("https://sogo.uni-heidelberg.de/", "sogo.uni-heidelberg.de"), "DIRECT");
  assert.ok(U.buildPac(["a.de"], 10800).includes("SOCKS5 127.0.0.1:10800"));
});

test("hostPatterns und statusUrl", () => {
  assert.deepEqual(U.hostPatterns(["a.de"]), ["*://a.de/*", "*://*.a.de/*"]);
  assert.equal(U.statusUrl(1081), "http://127.0.0.1:1081/status.json");
  assert.equal(U.statusUrl(1081, "/api/connect"), "http://127.0.0.1:1081/api/connect");
});
