-- The seeded "Savings rate" widget only summed spending. It becomes a saved
-- query: the share of income not spent over the selected date range.
UPDATE pinned_widgets
SET plan = json_object(
      'v', 2,
      'sql', 'SELECT CASE WHEN inc > 0 THEN (inc - spend) * 1.0 / inc END AS rate FROM (
  SELECT COALESCE(SUM(CASE WHEN t.type = ''income'' THEN t.amount_minor END), 0) AS inc,
         COALESCE(SUM(CASE WHEN t.type = ''expense'' THEN -t.amount_minor END), 0) AS spend
  FROM transactions t WHERE t.deleted_at IS NULL AND t.date BETWEEN :from AND :to)',
      'columns', json_object('rate', json_object('format', 'percent')),
      'caption', 'of income not spent'),
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE title = 'Savings rate' AND type = 'number'
  AND json_extract(plan, '$.metric') = 'spend'
  AND json_extract(plan, '$.breakdown') = 'none';
