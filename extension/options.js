const api = typeof browser !== "undefined" ? browser : chrome;
const isFirefox = typeof browser !== "undefined" && Boolean(api.proxy && api.proxy.onRequest);
const el = (id) => document.getElementById(id);

async function load() {
  const s = Object.assign({}, UniVpn.DEFAULTS, await api.storage.local.get(UniVpn.DEFAULTS));
  el("domains").value = s.domains.join("\n");
  el("socksPort").value = s.socksPort;
  el("httpPort").value = s.httpPort;
}

async function save() {
  const parsed = UniVpn.parseDomainList(el("domains").value);
  const socksPort = Number(el("socksPort").value);
  const httpPort = Number(el("httpPort").value);
  const result = el("result");
  result.className = "";
  result.textContent = "";
  const errors = parsed.errors.slice();
  for (const [name, port] of [["SOCKS-Port", socksPort], ["Status-Port", httpPort]]) {
    if (!Number.isInteger(port) || port < 1 || port > 65535) errors.push(name + " ist ungueltig");
  }
  if (errors.length) {
    result.className = "error";
    result.textContent = errors.join("\n");
    return;
  }
  // Firefox: Host-Permissions pro Domain anfragen (braucht die Nutzergeste dieses Klicks).
  let denied = false;
  if (isFirefox && parsed.domains.length) {
    const origins = UniVpn.hostPatterns(parsed.domains);
    denied = !(await api.permissions.request({ origins }));
  }
  await api.storage.local.set({ domains: parsed.domains, socksPort, httpPort });
  if (denied) {
    result.className = "error";
    result.textContent = "Ohne Freigabe fuer die Domains kann Firefox sie nicht umleiten.";
  } else {
    result.className = "ok";
    result.textContent = "Gespeichert";
  }
}

el("save").onclick = save;
load();
