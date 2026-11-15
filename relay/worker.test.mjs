// node --test relay/: the relay's answers, against an in-memory database.
import {test} from "node:test";
import assert from "node:assert/strict";
import {handle, cleanup, network} from "./worker.js";
import {d1} from "./d1.mjs";

const id = "0123456789abcdef0123456789abcdef";
const token = "ab".repeat(32);
const call = (env, method, path, {body, headers = {}, now = 1000} = {}) =>
  handle(new Request("http://relay" + path, {method, body, headers}), env, now);

test("pairing slot is read once and expires", async () => {
  const env = {DB: d1()};
  assert.equal((await call(env, "PUT", `/v1/pair/${id}`, {body: "hello"})).status, 201);
  assert.equal((await call(env, "PUT", `/v1/pair/${id}`, {body: "again"})).status, 409);
  const r = await call(env, "GET", `/v1/pair/${id}`);
  assert.equal(r.status, 200);
  assert.equal(await r.text(), "hello");
  assert.equal((await call(env, "GET", `/v1/pair/${id}`)).status, 404);
  await call(env, "PUT", `/v1/pair/${id}`, {body: "late"});
  assert.equal((await call(env, "GET", `/v1/pair/${id}`, {now: 1000 + 11 * 60 * 1000})).status, 404);
  assert.equal((await call(env, "PUT", `/v1/pair/${id}`, {body: "x".repeat(8 * 1024 + 1)})).status, 413);
  assert.equal((await call(env, "PUT", `/v1/pair/${id}`, {body: ""})).status, 413);
});

test("group versions, tokens and limits", async () => {
  const env = {DB: d1()};
  const auth = {Authorization: `Bearer ${token}`};
  assert.equal((await call(env, "GET", `/v1/group/${id}`, {headers: auth})).status, 404);
  assert.equal((await call(env, "GET", `/v1/group/${id}`)).status, 401);
  let r = await call(env, "PUT", `/v1/group/${id}`, {body: "one", headers: {...auth, "If-None-Match": "*"}});
  assert.equal(r.status, 200);
  assert.equal(r.headers.get("ETag"), '"1"');
  r = await call(env, "PUT", `/v1/group/${id}`, {body: "dup", headers: {...auth, "If-None-Match": "*"}});
  assert.equal(r.status, 412);
  r = await call(env, "PUT", `/v1/group/${id}`, {body: "two", headers: {...auth, "If-Match": '"1"'}});
  assert.equal(r.headers.get("ETag"), '"2"');
  assert.equal((await call(env, "PUT", `/v1/group/${id}`, {body: "old", headers: {...auth, "If-Match": '"1"'}})).status, 412);
  assert.equal((await call(env, "PUT", `/v1/group/${id}`, {body: "x", headers: auth})).status, 428);
  const other = {Authorization: `Bearer ${"cd".repeat(32)}`};
  assert.equal((await call(env, "GET", `/v1/group/${id}`, {headers: other})).status, 403);
  assert.equal((await call(env, "PUT", `/v1/group/${id}`, {body: "evil", headers: {...other, "If-Match": '"2"'}})).status, 403);
  r = await call(env, "GET", `/v1/group/${id}`, {headers: auth});
  assert.equal(await r.text(), "two");
  assert.equal(r.headers.get("ETag"), '"2"');
  // Reading keeps a group alive, once a day.
  await call(env, "GET", `/v1/group/${id}`, {headers: auth, now: 1000 + 300 * 24 * 60 * 60 * 1000});
  await cleanup(env, 1000 + 401 * 24 * 60 * 60 * 1000);
  assert.equal((await call(env, "GET", `/v1/group/${id}`, {headers: auth})).status, 200);
  // Closed after a final write.
  r = await call(env, "PUT", `/v1/group/${id}`, {body: "moved", headers: {...auth, "If-Match": '"2"', "Sync-Final": "1"}});
  assert.equal(r.headers.get("ETag"), '"3"');
  assert.equal((await call(env, "PUT", `/v1/group/${id}`, {body: "back", headers: {...auth, "If-Match": '"3"'}})).status, 410);
  assert.equal(await (await call(env, "GET", `/v1/group/${id}`, {headers: auth})).text(), "moved");
  const row = env.DB.db.prepare("SELECT token FROM groups").get();
  assert.notEqual(row.token, token, "only the hash is stored");
  await cleanup(env, 1000 + 701 * 24 * 60 * 60 * 1000);
  assert.equal((await call(env, "GET", `/v1/group/${id}`, {headers: auth})).status, 404);
});

test("a new group takes a whole document and goes soon unless a device uses it", async () => {
  const env = {DB: d1()};
  const auth = {Authorization: `Bearer ${token}`};
  const create = {...auth, "If-None-Match": "*"};
  const [unused, read] = ["1".repeat(32), "2".repeat(32)];
  assert.equal((await call(env, "PUT", `/v1/group/${id}`, {body: "x".repeat(256 * 1024 + 1), headers: create})).status, 413);
  // Removing a device moves the whole document, with every website ever listed, to a new group.
  assert.equal((await call(env, "PUT", `/v1/group/${id}`, {body: "x".repeat(200 * 1024), headers: create})).status, 200);
  await call(env, "PUT", `/v1/group/${unused}`, {body: "spam", headers: create});
  await call(env, "PUT", `/v1/group/${read}`, {body: "doc", headers: create});
  await call(env, "GET", `/v1/group/${read}`, {headers: auth, now: 1000 + 30 * 60 * 1000});
  const later = 1000 + 8 * 24 * 60 * 60 * 1000;
  await cleanup(env, later);
  for (const gone of [unused, id]) {
    assert.equal((await call(env, "GET", `/v1/group/${gone}`, {headers: auth, now: later})).status, 404);
  }
  assert.equal((await call(env, "GET", `/v1/group/${read}`, {headers: auth, now: later})).status, 200);
});

test("unknown paths", async () => {
  const env = {DB: d1()};
  for (const path of ["/", "/v1/pair/xyz", `/v1/other/${id}`, `/v2/pair/${id}`, `/v1/pair/${id}/x`]) {
    assert.equal((await call(env, "GET", path)).status, 404, path);
  }
  assert.equal((await call(env, "DELETE", `/v1/pair/${id}`)).status, 405);
});

const DAY = 24 * 60 * 60 * 1000;
const usage = (env) => Number(env.DB.db.prepare("SELECT bytes FROM usage").get().bytes);
const stored = (env) => Number(env.DB.db.prepare(
  "SELECT (SELECT total(length(body)) FROM pairs) + (SELECT total(length(body)) FROM groups) AS bytes").get().bytes);
const hex = (i) => i.toString(16).padStart(32, "0");

test("one network cannot fill the database", async () => {
  const env = {DB: d1()};
  const from = (ip) => ({Authorization: `Bearer ${token}`, "If-None-Match": "*", "CF-Connecting-IP": ip});
  const big = "x".repeat(256 * 1024);
  let made = 0;
  for (let i = 0; i < 50; i++) {
    const r = await call(env, "PUT", `/v1/group/${hex(i)}`, {body: big, headers: from("203.0.113.7")});
    if (r.status === 200) made++;
    else assert.equal(r.status, 429);
  }
  assert.equal(made, 16, "4 MiB a day");
  // Pairing slots count too; the same /48 is one network, another one is not.
  assert.equal((await call(env, "PUT", `/v1/pair/${id}`, {body: "x", headers: {"CF-Connecting-IP": "203.0.113.7"}})).status, 429);
  assert.equal((await call(env, "PUT", `/v1/group/${hex(100)}`, {body: big, headers: from("2001:db8:a::1")})).status, 200);
  const sameSite = await call(env, "PUT", `/v1/group/${hex(101)}`, {body: "x".repeat(4 * 1024 * 1024 - 256 * 1024), headers: from("2001:db8:a:ffff::2")});
  assert.equal(sameSite.status, 413, "larger than a group may be");
  for (let i = 102; i < 117; i++) await call(env, "PUT", `/v1/group/${hex(i)}`, {body: big, headers: from("2001:db8:a:ffff::2")});
  assert.equal((await call(env, "PUT", `/v1/group/${hex(117)}`, {body: big, headers: from("2001:db8:a:ffff::2")})).status, 429);
  assert.equal((await call(env, "PUT", `/v1/group/${hex(118)}`, {body: big, headers: from("2001:db8:b::1")})).status, 200);
  // The next day the network may add again.
  assert.equal((await call(env, "PUT", `/v1/group/${hex(200)}`, {body: big, headers: from("203.0.113.7"), now: 1000 + DAY})).status, 200);
  // Without CF-Connecting-IP (the local test server) nothing is counted.
  for (let i = 300; i < 320; i++) {
    assert.equal((await call(env, "PUT", `/v1/group/${hex(i)}`, {body: big, headers: {Authorization: `Bearer ${token}`, "If-None-Match": "*"}})).status, 200);
  }
});

test("a group that grows counts like new data", async () => {
  const env = {DB: d1()};
  const auth = {Authorization: `Bearer ${token}`, "CF-Connecting-IP": "198.51.100.1"};
  const big = "x".repeat(256 * 1024);
  for (let i = 0; i < 17; i++) {
    assert.equal((await call(env, "PUT", `/v1/group/${hex(i)}`, {body: "small", headers: {...auth, "If-None-Match": "*"}})).status, 200);
  }
  let grown = 0;
  for (let i = 0; i < 16; i++) {
    const r = await call(env, "PUT", `/v1/group/${hex(i)}`, {body: big, headers: {...auth, "If-Match": '"1"'}});
    if (r.status === 200) grown++;
    else assert.equal(r.status, 429);
  }
  assert.equal(grown, 15, "17 small groups and 15 grown to 256 KiB fill 4 MiB");
  // Writing the same size again, or less, adds nothing.
  assert.equal((await call(env, "PUT", `/v1/group/${hex(0)}`, {body: big, headers: {...auth, "If-Match": '"2"'}})).status, 200);
  assert.equal((await call(env, "PUT", `/v1/group/${hex(0)}`, {body: "less", headers: {...auth, "If-Match": '"3"'}})).status, 200);
});

test("a full database takes no new groups or slots, groups in use still change", async () => {
  const env = {DB: d1()};
  const auth = {Authorization: `Bearer ${token}`};
  assert.equal((await call(env, "PUT", `/v1/group/${id}`, {body: "doc", headers: {...auth, "If-None-Match": "*"}})).status, 200);
  env.DB.db.prepare("UPDATE usage SET bytes = 400 * 1000 * 1000 - 10").run();
  assert.equal((await call(env, "PUT", `/v1/group/${hex(1)}`, {body: "x".repeat(11), headers: {...auth, "If-None-Match": "*"}})).status, 507);
  assert.equal((await call(env, "PUT", `/v1/pair/${hex(2)}`, {body: "x".repeat(11)})).status, 507);
  assert.equal((await call(env, "PUT", `/v1/pair/${hex(2)}`, {body: "x".repeat(10)})).status, 201);
  assert.equal((await call(env, "PUT", `/v1/pair/${hex(2)}`, {body: "again"})).status, 409);
  // A group in use: up to the reserve for them, and smaller always.
  assert.equal((await call(env, "PUT", `/v1/group/${id}`, {body: "x".repeat(1000), headers: {...auth, "If-Match": '"1"'}})).status, 200);
  env.DB.db.prepare("UPDATE usage SET bytes = 450 * 1000 * 1000 - 10").run();
  assert.equal((await call(env, "PUT", `/v1/group/${id}`, {body: "x".repeat(1011), headers: {...auth, "If-Match": '"2"'}})).status, 507);
  assert.equal((await call(env, "PUT", `/v1/group/${id}`, {body: "x".repeat(500), headers: {...auth, "If-Match": '"2"'}})).status, 200);
  // The answers that were there before stay.
  assert.equal((await call(env, "PUT", `/v1/group/${id}`, {body: "y", headers: {...auth, "If-Match": '"2"'}})).status, 412);
  assert.equal((await call(env, "PUT", `/v1/group/${id}`, {body: "y", headers: {...auth, "If-None-Match": "*"}})).status, 412);
  assert.equal((await call(env, "PUT", `/v1/group/${hex(9)}`, {body: "y", headers: {...auth, "If-Match": '"1"'}})).status, 404);
});

test("the database knows its size", async () => {
  const env = {DB: d1()};
  const auth = {Authorization: `Bearer ${token}`};
  await call(env, "PUT", `/v1/group/${id}`, {body: "x".repeat(5000), headers: {...auth, "If-None-Match": "*"}});
  await call(env, "PUT", `/v1/group/${id}`, {body: "x".repeat(300), headers: {...auth, "If-Match": '"1"'}});
  await call(env, "PUT", `/v1/group/${hex(1)}`, {body: "y".repeat(70), headers: {...auth, "If-None-Match": "*"}});
  await call(env, "PUT", `/v1/pair/${hex(2)}`, {body: "z".repeat(40)});
  await call(env, "PUT", `/v1/pair/${hex(3)}`, {body: "z".repeat(20)});
  await call(env, "GET", `/v1/pair/${hex(3)}`);
  assert.equal(usage(env), 300 + 70 + 40);
  assert.equal(usage(env), stored(env));
  await cleanup(env, 1000 + 8 * DAY);  // the pairing slot, and the group nobody used again
  assert.equal(usage(env), 300);
  assert.equal(usage(env), stored(env));
});

test("only a hash of the network is kept, for one day", async () => {
  const env = {DB: d1()};
  await call(env, "PUT", `/v1/pair/${id}`, {body: "x", headers: {"CF-Connecting-IP": "192.0.2.44"}});
  const dump = JSON.stringify(env.DB.db.prepare("SELECT * FROM quota").all());
  assert.ok(!dump.includes("192.0.2"), dump);
  const secret = env.DB.db.prepare("SELECT secret FROM meta").get().secret;
  await call(env, "PUT", `/v1/pair/${hex(1)}`, {body: "x", headers: {"CF-Connecting-IP": "192.0.2.44"}, now: 1000 + DAY});
  assert.notEqual(env.DB.db.prepare("SELECT secret FROM meta").get().secret, secret, "a new secret each day");
  assert.equal(env.DB.db.prepare("SELECT count(*) AS n FROM quota").get().n, 1, "yesterday's count went");
  await cleanup(env, 1000 + 2 * DAY);
  assert.equal(env.DB.db.prepare("SELECT count(*) AS n FROM quota").get().n, 0);
});

test("network", () => {
  const from = (ip) => network(new Request("http://relay/", {headers: ip ? {"CF-Connecting-IP": ip} : {}}));
  assert.equal(from("203.0.113.7"), "203.0.113.7");
  assert.equal(from("2001:db8:abcd:12::1"), "2001:db8:abcd::/48");
  assert.equal(from("2001:0DB8:00ab:ffff:1:2:3:4"), "2001:db8:ab::/48");
  assert.equal(from("2001:db8::"), "2001:db8:0::/48");
  assert.equal(from("::1"), "0:0:0::/48");
  assert.equal(from(""), "");
});
