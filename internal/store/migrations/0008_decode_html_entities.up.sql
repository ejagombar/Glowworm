-- Chase exports text HTML-escaped ("Marks &amp; Spencer"). Imports now
-- decode it; this decodes what was imported before. dedupe_hash is left as
-- it was: re-imports also try the hash of the escaped description. Learnt
-- category rules, which hold the normalised text, are rebuilt at startup.
UPDATE transactions SET
    description = replace(replace(replace(replace(replace(replace(replace(description, '&quot;', '"'), '&#34;', '"'), '&#39;', ''''), '&apos;', ''''), '&lt;', '<'), '&gt;', '>'), '&amp;', '&'),
    merchant = replace(replace(replace(replace(replace(replace(replace(merchant, '&quot;', '"'), '&#34;', '"'), '&#39;', ''''), '&apos;', ''''), '&lt;', '<'), '&gt;', '>'), '&amp;', '&'),
    source_text = replace(replace(replace(replace(replace(replace(replace(source_text, '&quot;', '"'), '&#34;', '"'), '&#39;', ''''), '&apos;', ''''), '&lt;', '<'), '&gt;', '>'), '&amp;', '&'),
    destination_text = replace(replace(replace(replace(replace(replace(replace(destination_text, '&quot;', '"'), '&#34;', '"'), '&#39;', ''''), '&apos;', ''''), '&lt;', '<'), '&gt;', '>'), '&amp;', '&')
WHERE description LIKE '%&%;%' OR merchant LIKE '%&%;%' OR source_text LIKE '%&%;%' OR destination_text LIKE '%&%;%';
UPDATE import_staging SET
    description = replace(replace(replace(replace(replace(replace(replace(description, '&quot;', '"'), '&#34;', '"'), '&#39;', ''''), '&apos;', ''''), '&lt;', '<'), '&gt;', '>'), '&amp;', '&'),
    source_text = replace(replace(replace(replace(replace(replace(replace(source_text, '&quot;', '"'), '&#34;', '"'), '&#39;', ''''), '&apos;', ''''), '&lt;', '<'), '&gt;', '>'), '&amp;', '&'),
    destination_text = replace(replace(replace(replace(replace(replace(replace(destination_text, '&quot;', '"'), '&#34;', '"'), '&#39;', ''''), '&apos;', ''''), '&lt;', '<'), '&gt;', '>'), '&amp;', '&')
WHERE description LIKE '%&%;%' OR source_text LIKE '%&%;%' OR destination_text LIKE '%&%;%';

