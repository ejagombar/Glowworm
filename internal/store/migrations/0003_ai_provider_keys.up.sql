-- Several AI providers can be connected at once, each with its own key and
-- endpoint, and each model role remembers which provider its model is from.
-- The one provider set up so far moves across, and both roles point at it.
CREATE TABLE ai_provider_keys (
  provider    TEXT PRIMARY KEY,
  key_cipher  TEXT NOT NULL DEFAULT '',
  base_url    TEXT NOT NULL DEFAULT '',
  updated_at  TEXT NOT NULL
);

INSERT INTO ai_provider_keys (provider, key_cipher, base_url, updated_at)
  SELECT provider, key_cipher, base_url, updated_at FROM ai_provider_config WHERE provider <> '';

UPDATE ai_provider_config
   SET models = json_set(CASE WHEN json_valid(models) THEN models ELSE '{}' END,
                         '$.queries_provider', provider,
                         '$.categorisation_provider', provider),
       provider = '', key_cipher = '', base_url = ''
 WHERE provider <> '';
