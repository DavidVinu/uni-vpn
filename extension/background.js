// Hintergrund: in Chrome ein Service Worker (laedt common.js per importScripts),
// in Firefox eine Event Page (common.js kommt aus background.scripts).
if (typeof importScripts === "function" && typeof UniVpn === "undefined") {
  importScripts("common.js");
}
const api = typeof browser !== "undefined" ? browser : chrome;
const hasOnRequest = Boolean(api.proxy && api.proxy.onRequest);

let settings = null;
async function loadSettings() {
  const stored = await api.storage.local.get(UniVpn.DEFAULTS);
  settings = Object.assign({}, UniVpn.DEFAULTS, stored);
  return settings;
}
const settingsReady = loadSettings();

// Firefox: pro Request entscheiden. Listener synchron auf oberster Ebene, sonst weckt er die Event Page nicht.
if (hasOnRequest) {
  api.proxy.onRequest.addListener(
    async (details) => {
      const s = settings || (await settingsReady);
      let host = "";
      try {
        host = new URL(details.url).hostname;
      } catch (e) {
        return { type: "direct" };
      }
      if (s.enabled && UniVpn.matches(host, s.domains)) {
        return { type: "socks", host: "127.0.0.1", port: s.socksPort, proxyDNS: true };
      }
      return { type: "direct" };
    },
    { urls: ["<all_urls>"] }
  );
  api.proxy.onError.addListener((error) => console.error("uni-vpn proxy:", error));
}

// Chrome: PAC fuer das ganze Profil setzen. Ersetzt eine etwaige Systemproxy-Einstellung.
async function applyChromeProxy() {
  if (hasOnRequest || !api.proxy || !api.proxy.settings) return;
  const s = await loadSettings();
  if (!s.enabled) {
    await api.proxy.settings.clear({ scope: "regular" });
    return;
  }
  const pac = UniVpn.buildPac(s.domains, s.socksPort);
  await api.proxy.settings.set({ value: { mode: "pac_script", pacScript: { data: pac } }, scope: "regular" });
}

api.runtime.onInstalled.addListener(() => { applyChromeProxy(); });
api.runtime.onStartup.addListener(() => { applyChromeProxy(); });
api.storage.onChanged.addListener((changes, area) => {
  if (area === "local") loadSettings().then(applyChromeProxy);
});
applyChromeProxy();
