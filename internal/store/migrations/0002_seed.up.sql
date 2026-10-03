-- Glowworm 0002_seed — system categories + default settings (doc 02 §11)

INSERT INTO categories (id, name, color, type, is_system, sort_order, created_at, updated_at) VALUES
  ('c-system-income',    'Income',            '#0e7a4d', 'income',   1, 0, '1970-01-01T00:00:00Z', '1970-01-01T00:00:00Z'),
  ('c-system-internal',  'Internal transfer', '#2e9d8f', 'internal', 1, 0, '1970-01-01T00:00:00Z', '1970-01-01T00:00:00Z');

INSERT INTO settings (key, value, updated_at) VALUES
  ('theme',                    '"system"',    '1970-01-01T00:00:00Z'),
  ('currency',                 '"GBP"',       '1970-01-01T00:00:00Z'),
  ('user_name',                '""',          '1970-01-01T00:00:00Z'),
  ('default_period',           '"last-12m"',  '1970-01-01T00:00:00Z'),
  ('privacy_default',          'false',       '1970-01-01T00:00:00Z'),
  ('setup_completed',          'false',       '1970-01-01T00:00:00Z'),
  ('cat_local_threshold',      '0.75',        '1970-01-01T00:00:00Z'),
  ('cat_ai_threshold',         '0.8',         '1970-01-01T00:00:00Z'),
  ('cat_min_examples',         '3',           '1970-01-01T00:00:00Z'),
  ('transfer_threshold',       '0.7',         '1970-01-01T00:00:00Z'),
  ('transfer_date_window_days','4',           '1970-01-01T00:00:00Z'),
  ('ai_daily_token_cap',       '0',           '1970-01-01T00:00:00Z');

INSERT INTO audit_paths (id, parent_path_id, fork_entry_id, label, is_active, created_at) VALUES
  ('main', NULL, NULL, 'Main', 1, '1970-01-01T00:00:00Z');

INSERT INTO ai_provider_config (id, provider, models, base_url, key_cipher, updated_at) VALUES
  (1, '', '{}', '', '', '1970-01-01T00:00:00Z');
