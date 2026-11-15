// The sync relay (docs/sync.md): a Cloudflare Worker with one D1 database. It keeps
// ciphertext under random ids and never sees a key: pairing slots for a code on screen, and
// one blob per sync group with a version for compare-and-swap. internal/devsync is the client,
// internal/devsync/relaytest the same server in memory for the Go tests.

const MAX_PAIR = 8 * 1024;
// Also for a new group: removing a device moves the whole document into one.
const MAX_GROUP = 256 * 1024;
const PAIR_LIFETIME = 10 * 60 * 1000;
const DAY = 24 * 60 * 60 * 1000;
// A group no device read or wrote for this long is gone. Reads count once a day.
const GROUP_LIFETIME = 400 * DAY;
// A new group lives a week until a device reads or writes it: the one that made it reads it
// again within half an hour, a group made only to fill the database is gone soon.
const NEW_GROUP_LIFETIME = 7 * DAY;
// The database's size limit is 500 MB on the free plan. New groups and pairing slots stop
// below STORE_LIMIT, so that groups in use can still be written (up to STORE_HARD) when
// someone filled it. NETWORK_DAY is what one network may add in a day (new groups, pairing
// slots, groups that grow): about 2,000 pairings, but a few dozen requests cannot take the
// space for everyone. A network is an IPv4 address or an IPv6 /48 (one site).
const STORE_LIMIT = 400 * 1000 * 1000;
const STORE_HARD = 450 * 1000 * 1000;
const NETWORK_DAY = 4 * 1024 * 1024;
const ID = /^[0-9a-f]{32}$/;
const TOKEN = /^Bearer ([0-9a-f]{64})$/;

const text = (status, body) => new Response(body, {status, headers: {"Content-Type": "text/plain"}});

async function sha256(value) {
  const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(value));
  return [...new Uint8Array(digest)].map((b) => b.toString(16).padStart(2, "0")).join("");
}

// The body as an ArrayBuffer (D1 stores that as a BLOB), or null when it is empty or larger
// than limit.
async function body(request, limit) {
  const length = Number(request.headers.get("Content-Length") || "0");
  if (length > limit) return null;
  const data = await request.arrayBuffer();
  return data.byteLength === 0 || data.byteLength > limit ? null : data;
}

const etag = (version) => ({ETag: `"${version}"`});
const full = () => text(507, "full");
const busy = () => text(429, "too many new data from this network today, try again tomorrow");

// network is where a request comes from (Cloudflare's CF-Connecting-IP), "" without it (the
// local test server).
export function network(request) {
  const ip = request.headers.get("CF-Connecting-IP") || "";
  if (!ip.includes(":")) return ip;
  const [head, tail = ""] = ip.toLowerCase().split("::");
  const left = head ? head.split(":") : [];
  const right = tail ? tail.split(":") : [];
  const groups = [...left, ...Array(Math.max(0, 8 - left.length - right.length)).fill("0"), ...right];
  return groups.slice(0, 3).map((g) => g.replace(/^0+(?=.)/, "")).join(":") + "::/48";
}

// charge counts bytes against the network's allowance for today; false when it is used up.
// Only a secret of the day and a hash of the network are kept, and only for that day:
// yesterday's counts and secret go with the first request of a new day (and the hourly cron).
async function charge(env, request, bytes, now) {
  const where = network(request);
  if (!where || bytes <= 0) return true;
  const day = Math.floor(now / DAY);
  let meta = await env.DB.prepare("SELECT day, secret FROM meta WHERE id = 1").first();
  if (!meta || meta.day !== day) {
    const secret = [...crypto.getRandomValues(new Uint8Array(32))].map((b) => b.toString(16).padStart(2, "0")).join("");
    await env.DB.batch([
      env.DB.prepare("INSERT INTO meta (id, day, secret) VALUES (1, ?1, ?2) " +
        "ON CONFLICT (id) DO UPDATE SET day = ?1, secret = ?2 WHERE meta.day < ?1").bind(day, secret),
      env.DB.prepare("DELETE FROM quota WHERE day < ?1").bind(day),
    ]);
    meta = await env.DB.prepare("SELECT day, secret FROM meta WHERE id = 1").first();
  }
  const key = await sha256(meta.secret + " " + where);
  const row = await env.DB.prepare(
    "INSERT INTO quota (key, day, bytes) VALUES (?1, ?2, ?3) " +
    "ON CONFLICT (key) DO UPDATE SET bytes = bytes + ?3 WHERE quota.bytes + ?3 <= ?4 RETURNING bytes",
  ).bind(key, meta.day, bytes, NETWORK_DAY).first();
  return row !== null;
}

async function pair(request, env, id, now) {
  if (request.method === "PUT") {
    const data = await body(request, MAX_PAIR);
    if (!data) return text(413, "too large");
    if (!await charge(env, request, data.byteLength, now)) return busy();
    // A slot is written once; an expired one may be taken again.
    const result = await env.DB.prepare(
      "INSERT INTO pairs (id, body, expires) SELECT ?1, ?2, ?3 WHERE (SELECT bytes FROM usage) + length(?2) <= ?5 " +
      "ON CONFLICT (id) DO UPDATE SET body = ?2, expires = ?3 WHERE pairs.expires < ?4",
    ).bind(id, data, now + PAIR_LIFETIME, now, STORE_LIMIT).run();
    if (result.meta.changes) return text(201, "");
    const taken = await env.DB.prepare("SELECT 1 FROM pairs WHERE id = ?1 AND expires >= ?2").bind(id, now).first();
    return taken ? text(409, "exists") : full();
  }
  if (request.method === "GET") {
    // Read once: the slot is gone with the answer.
    const row = await env.DB.prepare("DELETE FROM pairs WHERE id = ?1 AND expires >= ?2 RETURNING body")
      .bind(id, now).first();
    if (!row) return text(404, "not found");
    return new Response(new Uint8Array(row.body), {headers: {"Content-Type": "application/octet-stream"}});
  }
  return text(405, "method not allowed");
}

async function group(request, env, id, now) {
  const auth = TOKEN.exec(request.headers.get("Authorization") || "");
  if (!auth) return text(401, "unauthorized");
  const token = await sha256(auth[1]);
  // Why a write changed nothing; version: the one it was for (none for a create).
  const denied = async (version) => {
    const row = await env.DB.prepare("SELECT token, final, version FROM groups WHERE id = ?1").bind(id).first();
    if (!row) return version === undefined ? full() : text(404, "not found");
    if (row.token !== token) return text(403, "forbidden");
    if (row.final) return text(410, "closed");
    return version === undefined || row.version !== version ? text(412, "changed") : full();
  };
  if (request.method === "GET") {
    const row = await env.DB.prepare("SELECT token, version, body, updated FROM groups WHERE id = ?1").bind(id).first();
    if (!row) return text(404, "not found");
    if (row.token !== token) return text(403, "forbidden");
    if (row.updated < now - DAY) {
      await env.DB.prepare("UPDATE groups SET updated = ?1 WHERE id = ?2").bind(now, id).run();
    }
    return new Response(new Uint8Array(row.body),
      {headers: {"Content-Type": "application/octet-stream", ...etag(row.version)}});
  }
  if (request.method === "PUT") {
    const create = request.headers.get("If-None-Match") === "*";
    const data = await body(request, MAX_GROUP);
    if (!data) return text(413, "too large");
    if (create) {
      if (!await charge(env, request, data.byteLength, now)) return busy();
      // updated as if last used long ago: cleanup takes it after NEW_GROUP_LIFETIME unless used.
      const row = await env.DB.prepare(
        "INSERT INTO groups (id, token, version, body, updated) SELECT ?1, ?2, 1, ?3, ?4 " +
        "WHERE (SELECT bytes FROM usage) + length(?3) <= ?5 ON CONFLICT (id) DO NOTHING RETURNING version",
      ).bind(id, token, data, now - GROUP_LIFETIME + NEW_GROUP_LIFETIME, STORE_LIMIT).first();
      return row ? new Response(null, {status: 200, headers: etag(row.version)}) : denied();
    }
    const match = /^"(\d+)"$/.exec(request.headers.get("If-Match") || "");
    if (!match) return text(428, "If-Match or If-None-Match required");
    const version = Number(match[1]);
    // What a group grows by counts like new data.
    const old = await env.DB.prepare("SELECT length(body) AS size FROM groups WHERE id = ?1 AND token = ?2")
      .bind(id, token).first();
    if (old && !await charge(env, request, data.byteLength - old.size, now)) return busy();
    // Sync-Final closes the group after this write: a device removed from it still knows
    // the token but must not replace the forwarding note (docs/sync.md, Removing a device).
    const row = await env.DB.prepare(
      "UPDATE groups SET body = ?1, version = version + 1, updated = ?2, final = ?6 " +
      "WHERE id = ?3 AND token = ?4 AND version = ?5 AND final = 0 " +
      "AND (SELECT bytes FROM usage) + length(?1) - length(body) <= ?7 RETURNING version",
    ).bind(data, now, id, token, version, request.headers.get("Sync-Final") === "1" ? 1 : 0, STORE_HARD).first();
    return row ? new Response(null, {status: 200, headers: etag(row.version)}) : denied(version);
  }
  return text(405, "method not allowed");
}

export async function handle(request, env, now = Date.now()) {
  const parts = new URL(request.url).pathname.split("/");
  if (parts.length !== 4 || parts[0] !== "" || parts[1] !== "v1" || !ID.test(parts[3])) return text(404, "not found");
  if (parts[2] === "pair") return pair(request, env, parts[3], now);
  if (parts[2] === "group") return group(request, env, parts[3], now);
  return text(404, "not found");
}

export async function cleanup(env, now = Date.now()) {
  await env.DB.batch([
    env.DB.prepare("DELETE FROM pairs WHERE expires < ?1").bind(now),
    env.DB.prepare("DELETE FROM groups WHERE updated < ?1").bind(now - GROUP_LIFETIME),
    env.DB.prepare("DELETE FROM quota WHERE day < ?1").bind(Math.floor(now / DAY)),
  ]);
}

export default {
  async fetch(request, env) {
    try {
      return await handle(request, env);
    } catch (e) {
      console.error(e);
      return text(500, "internal error");
    }
  },
  async scheduled(event, env) {
    await cleanup(env);
  },
};
