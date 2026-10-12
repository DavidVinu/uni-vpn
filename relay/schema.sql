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
