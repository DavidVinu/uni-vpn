// node --test relay/: the relay's answers, against an in-memory database.
import {test} from "node:test";
import assert from "node:assert/strict";
import {handle, cleanup} from "./worker.js";
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
