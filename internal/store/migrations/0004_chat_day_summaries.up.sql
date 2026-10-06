-- Glowworm 0004_chat_day_summaries — sidebar day summaries (doc 10 §8)
-- One row per local calendar day the chat was used; the summary is generated
-- by the LLM at midnight (or on catch-up at next load) and capped at 50 chars.

CREATE TABLE chat_day_summaries (
  day           TEXT PRIMARY KEY,        -- YYYY-MM-DD (server-local)
  summary       TEXT NOT NULL,
  message_count INTEGER NOT NULL DEFAULT 0,
  created_at    TEXT NOT NULL,
  updated_at    TEXT NOT NULL
);
