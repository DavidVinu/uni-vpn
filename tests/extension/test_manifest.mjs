import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync, existsSync } from "node:fs";

const dir = new URL("../../extension/", import.meta.url);
const manifest = JSON.parse(readFileSync(new URL("manifest.json", dir), "utf8"));

test("Manifest V3 fuer beide Browser", () => {
  assert.equal(manifest.manifest_version, 3);
  assert.deepEqual(manifest.background.scripts, ["common.js", "background.js"]);
  assert.equal(manifest.background.service_worker, "background.js");
  assert.equal(manifest.background.persistent, undefined);
  assert.ok(manifest.permissions.includes("proxy"));
  assert.ok(manifest.permissions.includes("storage"));
  assert.ok(!manifest.permissions.includes("nativeMessaging"));
  const gecko = manifest.browser_specific_settings.gecko;
  assert.equal(gecko.id, "uni-vpn@davidvinu.de");
  assert.equal(gecko.strict_min_version, "140.0");
  assert.deepEqual(gecko.data_collection_permissions, { required: ["none"] });
  assert.match(manifest.key, /^MIIB/);
});

test("Alle referenzierten Dateien existieren", () => {
  const files = ["common.js", "background.js", "popup.html", "popup.js", "options.html", "options.js", "style.css",
    ...Object.values(manifest.icons), manifest.action.default_popup, manifest.options_ui.page];
  for (const f of files) assert.ok(existsSync(new URL(f, dir)), f);
});
