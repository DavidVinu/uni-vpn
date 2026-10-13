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

async function pair(request, env, id, now) {
  if (request.method === "PUT") {
    const data = await body(request, MAX_PAIR);
    if (!data) return text(413, "too large");
    // A slot is written once; an expired one may be taken again.
    const result = await env.DB.prepare(
      "INSERT INTO pairs (id, body, expires) VALUES (?1, ?2, ?3) " +
      "ON CONFLICT (id) DO UPDATE SET body = ?2, expires = ?3 WHERE pairs.expires < ?4",
    ).bind(id, data, now + PAIR_LIFETIME, now).run();
    return result.meta.changes ? text(201, "") : text(409, "exists");
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
  const denied = async () => {
    const row = await env.DB.prepare("SELECT token, final FROM groups WHERE id = ?1").bind(id).first();
    if (!row) return text(404, "not found");
    if (row.token !== token) return text(403, "forbidden");
    return row.final ? text(410, "closed") : text(412, "changed");
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
      // updated as if last used long ago: cleanup takes it after NEW_GROUP_LIFETIME unless used.
      const row = await env.DB.prepare(
        "INSERT INTO groups (id, token, version, body, updated) VALUES (?1, ?2, 1, ?3, ?4) " +
        "ON CONFLICT (id) DO NOTHING RETURNING version",
      ).bind(id, token, data, now - GROUP_LIFETIME + NEW_GROUP_LIFETIME).first();
      return row ? new Response(null, {status: 200, headers: etag(row.version)}) : denied();
    }
    const match = /^"(\d+)"$/.exec(request.headers.get("If-Match") || "");
    if (!match) return text(428, "If-Match or If-None-Match required");
    // Sync-Final closes the group after this write: a device removed from it still knows
    // the token but must not replace the forwarding note (docs/sync.md, Removing a device).
    const row = await env.DB.prepare(
      "UPDATE groups SET body = ?1, version = version + 1, updated = ?2, final = ?6 " +
      "WHERE id = ?3 AND token = ?4 AND version = ?5 AND final = 0 RETURNING version",
    ).bind(data, now, id, token, Number(match[1]), request.headers.get("Sync-Final") === "1" ? 1 : 0).first();
    return row ? new Response(null, {status: 200, headers: etag(row.version)}) : denied();
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
