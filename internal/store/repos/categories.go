package repos

import (
	"database/sql"

	"glowworm/internal/core"
	"glowworm/internal/store"
	"glowworm/internal/util"
)

// Categories is the categories repo (doc 02 §4).
type Categories struct{ DB *store.DB }

const categoryCols = `id, name, color, type, is_system, sort_order, created_at, updated_at, deleted_at`

func scanCategory(sc Scanner) (*core.Category, error) {
	var c core.Category
	var isSys, deletedAt sql.NullString
	err := sc.Scan(&c.ID, &c.Name, &c.Color, &c.Type, &isSys, &c.SortOrder, &c.CreatedAt, &c.UpdatedAt, &deletedAt)
	if err != nil {
		return nil, err
	}
	c.IsSystem = isSys.Valid && isSys.String == "1"
	if deletedAt.Valid {
		s := deletedAt.String
		c.DeletedAt = &s
	}
	return &c, nil
}

// List returns live categories (system first, then sort_order, name).
func (r *Categories) List() ([]*core.Category, error) {
	rows, err := r.DB.Query(`SELECT ` + categoryCols + ` FROM categories WHERE deleted_at IS NULL
		ORDER BY is_system DESC, sort_order ASC, name COLLATE NOCASE ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*core.Category
	for rows.Next() {
		c, err := scanCategory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Get returns one live category.
func (r *Categories) Get(id string) (*core.Category, error) {
	return scanCategory(r.DB.QueryRow(`SELECT `+categoryCols+` FROM categories WHERE id = ? AND deleted_at IS NULL`, id))
}

// ByName returns one live category by case-insensitive name.
func (r *Categories) ByName(name string) (*core.Category, error) {
	return scanCategory(r.DB.QueryRow(`SELECT `+categoryCols+` FROM categories WHERE name = ? COLLATE NOCASE AND deleted_at IS NULL`, name))
}

// Create inserts a category (tx may be nil).
func (r *Categories) Create(tx *sql.Tx, c *core.Category) error {
	c.ID = util.NewID()
	c.CreatedAt = util.NowStr()
	c.UpdatedAt = c.CreatedAt
	sys := 0
	if c.IsSystem {
		sys = 1
	}
	_, err := exec(r.DB, tx, `INSERT INTO categories (id, name, color, type, is_system, sort_order, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?)`, c.ID, c.Name, c.Color, c.Type, sys, c.SortOrder, c.CreatedAt, c.UpdatedAt)
	return err
}

// CreateWithID inserts with a caller-supplied id (system seeds / restores).
func (r *Categories) CreateWithID(tx *sql.Tx, c *core.Category) error {
	c.CreatedAt = util.NowStr()
	c.UpdatedAt = c.CreatedAt
	sys := 0
	if c.IsSystem {
		sys = 1
	}
	_, err := exec(r.DB, tx, `INSERT INTO categories (id, name, color, type, is_system, sort_order, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?)`, c.ID, c.Name, c.Color, c.Type, sys, c.SortOrder, c.CreatedAt, c.UpdatedAt)
	return err
}

// Update persists name/color/type (tx may be nil).
func (r *Categories) Update(tx *sql.Tx, c *core.Category) error {
	c.UpdatedAt = util.NowStr()
	sys := 0
	if c.IsSystem {
		sys = 1
	}
	_, err := exec(r.DB, tx, `UPDATE categories SET name=?, color=?, type=?, is_system=?, updated_at=? WHERE id=? AND deleted_at IS NULL`,
		c.Name, c.Color, c.Type, sys, c.UpdatedAt, c.ID)
	return err
}

// Delete soft-deletes a user category (tx may be nil).
func (r *Categories) Delete(tx *sql.Tx, id string) error {
	_, err := exec(r.DB, tx, `UPDATE categories SET deleted_at = ? WHERE id = ? AND is_system = 0`, util.NowStr(), id)
	return err
}

// Restore un-deletes (path restore).
func (r *Categories) Restore(tx *sql.Tx, id string) error {
	_, err := exec(r.DB, tx, `UPDATE categories SET deleted_at = NULL WHERE id = ?`, id)
	return err
}

// NextSortOrder appends to the end of the list.
func (r *Categories) NextSortOrder() int {
	var n int
	_ = r.DB.QueryRow(`SELECT COALESCE(MAX(sort_order),0)+1 FROM categories WHERE deleted_at IS NULL`).Scan(&n)
	return n
}
