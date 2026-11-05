-- Passkeys (Face ID, Touch ID and the like) that unlock Glowworm on a
-- device that has its lock turned on. credential is the go-webauthn
-- Credential as JSON: the public key, sign count and flags.
CREATE TABLE passkeys (
  id             TEXT PRIMARY KEY,
  user_id        TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  credential_id  TEXT NOT NULL UNIQUE,
  credential     TEXT NOT NULL,
  name           TEXT NOT NULL,
  created_at     TEXT NOT NULL,
  last_used_at   TEXT
);
CREATE INDEX passkeys_user ON passkeys(user_id);
