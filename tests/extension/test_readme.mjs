import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

const readme = readFileSync(new URL("../../README.md", import.meta.url), "utf8");
const rows = readme.split("\n").filter((l) => l.startsWith("| Popup:"));

test("Feststellung 27: Support-Satz aus der Spec steht im Readme", () => {
  assert.ok(readme.includes("Alles andere kann funktionieren, kein Support."));
});

test("Feststellung 27: Firefox-Punkt nennt beide Dialoge", () => {
  const firefox = readme.split("\n- Firefox:")[1]?.split("\n\n")[0] ?? "";
  assert.match(firefox, /Freigabe/);
  assert.match(firefox, /Speichern/);
  assert.match(firefox, /In privaten Fenstern ausfuehren/);
  assert.match(firefox, /about:addons/);
});

test("Feststellung 27: Fehlertabelle erklaert die beiden Firefox-Warnungen des Popups", () => {
  const freigabe = rows.find((r) => r.includes("Freigabe fuer die gelisteten Domains fehlt"));
  assert.ok(freigabe, "Zeile fuer fehlende Freigabe");
  assert.match(freigabe, /Freigeben/);
  const privat = rows.find((r) => r.includes("In privaten Fenstern nicht aktiv"));
  assert.ok(privat, "Zeile fuer private Fenster");
  assert.match(privat, /about:addons/);
});

test("Readme ohne Em-Dashes", () => {
  assert.ok(!readme.includes("—"));
});
