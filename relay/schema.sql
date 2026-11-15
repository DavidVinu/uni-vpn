-- The relay's database (worker.js). Blobs are ciphertext; token is the SHA-256 of the
-- group's bearer token, which only the group's devices can derive.
CREATE TABLE IF NOT EXISTS pairs (
  id TEXT PRIMARY KEY,
  body BLOB NOT NULL,
  expires INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS groups (
  id TEXT PRIMARY KEY,
  token TEXT NOT NULL,
  version INTEGER NOT NULL,
  body BLOB NOT NULL,
  updated INTEGER NOT NULL,
  final INTEGER NOT NULL DEFAULT 0
);
-- usage: the bytes of every blob, kept by the triggers below, so that a write can see at once
-- whether the database is full (worker.js, STORE_LIMIT). Counted from the rows on the first run.
CREATE TABLE IF NOT EXISTS usage (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  bytes INTEGER NOT NULL
);
INSERT OR IGNORE INTO usage (id, bytes)
  SELECT 1, (SELECT total(length(body)) FROM pairs) + (SELECT total(length(body)) FROM groups);
CREATE TRIGGER IF NOT EXISTS pairs_added AFTER INSERT ON pairs
  BEGIN UPDATE usage SET bytes = bytes + length(NEW.body) WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS pairs_changed AFTER UPDATE OF body ON pairs
  BEGIN UPDATE usage SET bytes = bytes + length(NEW.body) - length(OLD.body) WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS pairs_removed AFTER DELETE ON pairs
  BEGIN UPDATE usage SET bytes = bytes - length(OLD.body) WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS groups_added AFTER INSERT ON groups
  BEGIN UPDATE usage SET bytes = bytes + length(NEW.body) WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS groups_changed AFTER UPDATE OF body ON groups
  BEGIN UPDATE usage SET bytes = bytes + length(NEW.body) - length(OLD.body) WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS groups_removed AFTER DELETE ON groups
  BEGIN UPDATE usage SET bytes = bytes - length(OLD.body) WHERE id = 1; END;
-- What each network added today (worker.js, NETWORK_DAY), under a hash with the day's secret
-- in meta. Both are replaced on the first request of a new day; the cron deletes old counts.
CREATE TABLE IF NOT EXISTS meta (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  day INTEGER NOT NULL,
  secret TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS quota (
  key TEXT PRIMARY KEY,
  day INTEGER NOT NULL,
  bytes INTEGER NOT NULL
);
