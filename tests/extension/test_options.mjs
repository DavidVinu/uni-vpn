import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import vm from "node:vm";

const common = readFileSync(new URL("../../extension/common.js", import.meta.url), "utf8");
const options = readFileSync(new URL("../../extension/options.js", import.meta.url), "utf8");
// Werte aus dem vm-Kontext haben fremde Prototypen; fuer deepEqual auf reine Daten bringen.
const plain = (x) => JSON.parse(JSON.stringify(x));

// Minimaler DOM- und API-Stub: options.js laeuft als Skript im Browser, nicht als Modul.
function makePage({ firefox = false, grant = true } = {}) {
  const els = {};
  for (const id of ["domains", "socksPort", "httpPort", "result", "save"]) {
    els[id] = { id, value: "", className: "", textContent: "", onclick: null };
  }
  const stored = {};
  const requests = [];
  const api = {
    storage: { local: {
      get: async (defaults) => Object.assign({}, defaults, stored),
      set: async (obj) => { Object.assign(stored, obj); },
    } },
    permissions: { request: async ({ origins }) => { requests.push(origins); return grant; } },
    proxy: firefox ? { onRequest: {} } : { settings: {} },
  };
  const ctx = vm.createContext({
    document: { getElementById: (id) => els[id] },
    chrome: api,
    ...(firefox ? { browser: api } : {}),
  });
  vm.runInContext(common, ctx);
  vm.runInContext(options, ctx);
  return { els, stored, requests, save: () => els.save.onclick() };
}

test("gueltige Eingabe speichert und meldet Gespeichert", async () => {
  const p = makePage();
  p.els.domains.value = "sogo.uni-heidelberg.de\n";
  p.els.socksPort.value = "1080";
  p.els.httpPort.value = "1081";
  await p.save();
  assert.deepEqual(plain(p.stored), { domains: ["sogo.uni-heidelberg.de"], socksPort: 1080, httpPort: 1081 });
  assert.equal(p.els.result.className, "ok");
  assert.equal(p.els.result.textContent, "Gespeichert");
});

test("ungueltige Eingabe meldet Fehler und speichert nicht", async () => {
  const p = makePage();
  p.els.domains.value = "https://bad.example/x\n";
  p.els.socksPort.value = "1080";
  p.els.httpPort.value = "70000";
  await p.save();
  assert.deepEqual(plain(p.stored), {});
  assert.equal(p.els.result.className, "error");
  assert.match(p.els.result.textContent, /Zeile 1/);
  assert.match(p.els.result.textContent, /Status-Port ist ungueltig/);
});

test("Feststellung 14: nach einem Fehler zeigt das naechste gueltige Speichern Gespeichert", async () => {
  const p = makePage();
  p.els.socksPort.value = "1080";
  p.els.httpPort.value = "1081";
  p.els.domains.value = "https://bad.example/x\n";
  await p.save();
  assert.equal(p.els.result.className, "error");
  p.els.domains.value = "sogo.uni-heidelberg.de\n";
  await p.save();
  assert.deepEqual(plain(p.stored.domains), ["sogo.uni-heidelberg.de"]);
  assert.equal(p.els.result.className, "ok");
  assert.equal(p.els.result.textContent, "Gespeichert");
});

test("Firefox: abgelehnte Freigabe meldet Fehler, spaetere Freigabe meldet Gespeichert", async () => {
  const p = makePage({ firefox: true, grant: false });
  p.els.socksPort.value = "1080";
  p.els.httpPort.value = "1081";
  p.els.domains.value = "sogo.uni-heidelberg.de\n";
  await p.save();
  assert.deepEqual(plain(p.requests), [["*://sogo.uni-heidelberg.de/*", "*://*.sogo.uni-heidelberg.de/*"]]);
  assert.equal(p.els.result.className, "error");
  assert.match(p.els.result.textContent, /Freigabe/);
  assert.deepEqual(plain(p.stored.domains), ["sogo.uni-heidelberg.de"]);

  // Zweiter Versuch mit erteilter Freigabe: gleiche Seite, gleicher Zustand des result-Elements.
  const q = makePage({ firefox: true, grant: true });
  q.els.result.className = "error";
  q.els.result.textContent = "alter Fehler";
  q.els.socksPort.value = "1080";
  q.els.httpPort.value = "1081";
  q.els.domains.value = "sogo.uni-heidelberg.de\n";
  await q.save();
  assert.equal(q.els.result.className, "ok");
  assert.equal(q.els.result.textContent, "Gespeichert");
});

test("Chrome fragt keine Host-Permissions an", async () => {
  const p = makePage({ firefox: false, grant: false });
  p.els.socksPort.value = "1080";
  p.els.httpPort.value = "1081";
  p.els.domains.value = "sogo.uni-heidelberg.de\n";
  await p.save();
  assert.deepEqual(plain(p.requests), []);
  assert.equal(p.els.result.textContent, "Gespeichert");
});
