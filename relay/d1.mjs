// D1's API over node:sqlite, enough for worker.js: prepare, bind, run, first, batch.
import {DatabaseSync} from "node:sqlite";
import {readFileSync} from "node:fs";

const value = (v) => (v instanceof ArrayBuffer ? new Uint8Array(v) : v);

export function d1() {
  const db = new DatabaseSync(":memory:");
  db.exec(readFileSync(new URL("schema.sql", import.meta.url), "utf8"));
  const statement = (sql, args = []) => ({
    bind: (...values) => statement(sql, values.map(value)),
    async run() { return {meta: {changes: Number(db.prepare(sql).run(...args).changes)}}; },
    async first() { return db.prepare(sql).get(...args) ?? null; },
  });
  return {
    db,
    prepare: (sql) => statement(sql),
    async batch(list) { return Promise.all(list.map((s) => s.run())); },
  };
}
