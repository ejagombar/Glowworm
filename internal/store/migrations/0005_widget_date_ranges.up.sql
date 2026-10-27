-- The example widgets were saved with fixed dates (the month they were
-- created), which the query engine then ignored in favour of the last 12
-- months. "— this month" widgets now name the rolling this-month range, and
-- Top merchants names none, so it follows the date range the user selects.
UPDATE pinned_widgets
SET plan = json_set(plan, '$.time_preset', 'this-month', '$.from', '', '$.to', '')
WHERE plan IS NOT NULL AND json_valid(plan) AND title LIKE '%— this month'
  AND json_extract(plan, '$.time_preset') IS NULL;

UPDATE pinned_widgets
SET plan = json_set(plan, '$.from', '', '$.to', '')
WHERE plan IS NOT NULL AND json_valid(plan) AND title = 'Top merchants'
  AND json_extract(plan, '$.breakdown') = 'merchant'
  AND json_extract(plan, '$.time_preset') IS NULL;
