-- What the user means by a category, in their own words. Only the
-- categorisation AI reads it; the app shows it nowhere but the edit form.
ALTER TABLE categories ADD COLUMN description TEXT NOT NULL DEFAULT '';
