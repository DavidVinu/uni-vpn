const api = typeof browser !== "undefined" ? browser : chrome;
const isFirefox = typeof browser !== "undefined" && Boolean(api.proxy && api.proxy.onRequest);
const el = (id) => document.getElementById(id);
let settings = null;

async function loadSettings() {
  settings = Object.assign({}, UniVpn.DEFAULTS, await api.storage.local.get(UniVpn.DEFAULTS));
}

function show(state, message) {
  el("dot").className = "dot " + state;
  el("message").textContent = message;
}

async function post(path) {
  await fetch(UniVpn.statusUrl(settings.httpPort, path), {
    method: "POST",
    headers: { "X-Uni-VPN": "1", "Content-Type": "application/json" },
    body: "{}",
  });
}

async function refresh() {
  try {
    const response = await fetch(UniVpn.statusUrl(settings.httpPort), { cache: "no-store" });
    const s = await response.json();
    if (s.protocol !== 1) {
      show("error", "Extension und Backend passen nicht zusammen (uni-vpn update)");
      return;
    }
    show(s.state, s.message);
    el("connect").disabled = ["connected", "connecting"].includes(s.state);
    el("disconnect").disabled = ["idle", "disconnecting"].includes(s.state);
    if (s.socks_port !== settings.socksPort) {
      settings.socksPort = s.socks_port;
      await api.storage.local.set({ socksPort: s.socks_port });
    }
  } catch (e) {
    show("error", "Daemon nicht erreichbar: uni-vpn doctor");
    el("connect").disabled = true;
    el("disconnect").disabled = true;
  }
}

function warn(text, action) {
  const p = document.createElement("p");
  p.className = "warn";
  p.textContent = text + " ";
  if (action) {
    const b = document.createElement("button");
    b.textContent = action.label;
    b.onclick = action.run;
    p.appendChild(b);
  }
  el("warnings").appendChild(p);
}

async function checkWarnings() {
  el("warnings").textContent = "";
  if (!settings.enabled) warn("Proxy-Regel ist ausgeschaltet.");
  if (isFirefox) {
    if (!(await api.extension.isAllowedIncognitoAccess())) {
      warn("In privaten Fenstern nicht aktiv (about:addons, Uni VPN, 'In privaten Fenstern ausfuehren').");
    }
    const origins = UniVpn.hostPatterns(settings.domains);
    if (origins.length && !(await api.permissions.contains({ origins }))) {
      warn("Freigabe fuer die gelisteten Domains fehlt.", {
        label: "Freigeben",
        run: () => api.permissions.request({ origins }).then(checkWarnings),
      });
    }
  } else if (api.proxy && api.proxy.settings) {
    const current = await api.proxy.settings.get({});
    if (!["controlled_by_this_extension", "controllable_by_this_extension"].includes(current.levelOfControl)) {
      warn("Proxy wird von einer anderen Extension oder Richtlinie gesteuert.");
    }
  }
}

async function main() {
  await loadSettings();
  el("enabled").checked = settings.enabled;
  el("enabled").onchange = async () => {
    await api.storage.local.set({ enabled: el("enabled").checked });
    await loadSettings();
    checkWarnings();
  };
  el("connect").onclick = () => post("/api/connect").then(refresh);
  el("disconnect").onclick = () => post("/api/disconnect").then(refresh);
  el("statuslink").onclick = (e) => { e.preventDefault(); api.tabs.create({ url: UniVpn.statusUrl(settings.httpPort, "/") }); };
  el("options").onclick = (e) => { e.preventDefault(); api.runtime.openOptionsPage(); };
  await refresh();
  await checkWarnings();
  setInterval(refresh, 2000);
}

main();
