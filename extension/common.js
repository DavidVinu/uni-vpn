// Gemeinsame Logik fuer Chrome, Firefox und die Node-Tests. Kein Modul, damit
// importScripts() (Chrome) und background.scripts (Firefox) dieselbe Datei laden.
(function (root) {
  "use strict";

  const DEFAULTS = {
    // cip.dmed: elearning-med bindet von dort matomo.js ein, der Host ist nur im Uni-Netz erreichbar.
    domains: ["sogo.uni-heidelberg.de", "elearning-med.uni-heidelberg.de", "cip.dmed.uni-heidelberg.de"],
    socksPort: 1080,
    httpPort: 1081,
    enabled: true,
  };

  const LABEL = "[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?";
  const HOST_RE = new RegExp("^(?=.{1,253}$)" + LABEL + "(?:\\." + LABEL + ")+$");

  function normalizeHost(value) {
    return String(value || "").trim().toLowerCase().replace(/\.$/, "");
  }

  function parseDomainList(text) {
    const domains = [];
    const errors = [];
    const seen = new Set();
    String(text || "").split(/\r?\n/).forEach((raw, index) => {
      let line = normalizeHost(raw.split("#")[0]);
      if (!line) return;
      line = line.replace(/^\*\./, "");
      if (!HOST_RE.test(line)) {
        errors.push("Zeile " + (index + 1) + ": '" + raw.trim() + "' ist kein Hostname");
        return;
      }
      if (!seen.has(line)) {
        seen.add(line);
        domains.push(line);
      }
    });
    return { domains: domains, errors: errors };
  }

  function matches(host, domains) {
    const h = normalizeHost(host);
    if (!h) return false;
    for (let i = 0; i < domains.length; i++) {
      const d = domains[i];
      if (h === d || (h.length > d.length && h.slice(-(d.length + 1)) === "." + d)) return true;
    }
    return false;
  }

  function buildPac(domains, port) {
    return [
      "function FindProxyForURL(url, host) {",
      "  var domains = " + JSON.stringify(domains) + ";",
      "  host = host.toLowerCase();",
      '  if (host.charAt(host.length - 1) === ".") host = host.slice(0, -1);',
      "  for (var i = 0; i < domains.length; i++) {",
      "    var d = domains[i];",
      '    if (host === d || (host.length > d.length && host.slice(-(d.length + 1)) === "." + d)) {',
      '      return "SOCKS5 127.0.0.1:' + Number(port) + '";',
      "    }",
      "  }",
      '  return "DIRECT";',
      "}",
      "",
    ].join("\n");
  }

  function hostPatterns(domains) {
    const out = [];
    for (const d of domains) out.push("*://" + d + "/*", "*://*." + d + "/*");
    return out;
  }

  function statusUrl(httpPort, path) {
    return "http://127.0.0.1:" + Number(httpPort) + (path || "/status.json");
  }

  root.UniVpn = { DEFAULTS, parseDomainList, matches, buildPac, hostPatterns, statusUrl };
})(typeof globalThis !== "undefined" ? globalThis : this);
