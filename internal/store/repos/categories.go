package repos

import (
	"database/sql"
	"errors"
	"strings"

	"github.com/ejagombar/Glowworm/internal/core"
	"github.com/ejagombar/Glowworm/internal/store"
	"github.com/ejagombar/Glowworm/internal/util"
)

type Categories struct{ DB *store.DB }

const categoryCols = `id, name, color, type, description, is_system, sort_order, created_at, updated_at, deleted_at`

func scanCategory(sc Scanner) (*core.Category, error) {
	var c core.Category
	var isSys, deletedAt sql.NullString
	err := sc.Scan(&c.ID, &c.Name, &c.Color, &c.Type, &c.Description, &isSys, &c.SortOrder, &c.CreatedAt, &c.UpdatedAt, &deletedAt)
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

func (r *Categories) Get(id string) (*core.Category, error) {
	return scanCategory(r.DB.QueryRow(`SELECT `+categoryCols+` FROM categories WHERE id = ? AND deleted_at IS NULL`, id))
}

func (r *Categories) ByName(name string) (*core.Category, error) {
	return scanCategory(r.DB.QueryRow(`SELECT `+categoryCols+` FROM categories WHERE name = ? COLLATE NOCASE AND deleted_at IS NULL`, name))
}

// ErrNameTaken is returned for a deleted category that can't come back
// because a live one has its name now.
var ErrNameTaken = errors.New("another category has that name now")

// setAside is what a deleted category's name ends in once a live one has
// taken the name.
func setAside(id string) string { return " (deleted " + id + ")" }

// freeName lets the category with this id take a name that a deleted one
// still holds: the name column is unique across both. The deleted one is
// kept under a name that Restore turns back.
func (r *Categories) freeName(tx *sql.Tx, name, id string) error {
	_, err := exec(r.DB, tx, `UPDATE categories SET name = name || ' (deleted ' || id || ')'
		WHERE name = ? COLLATE NOCASE AND id != ? AND deleted_at IS NOT NULL`, name, id)
	return err
}

func (r *Categories) Create(tx *sql.Tx, c *core.Category) error {
	c.ID = util.NewID()
	if err := r.freeName(tx, c.Name, c.ID); err != nil {
		return err
	}
	c.CreatedAt = util.NowStr()
	c.UpdatedAt = c.CreatedAt
	sys := 0
	if c.IsSystem {
		sys = 1
	}
	_, err := exec(r.DB, tx, `INSERT INTO categories (id, name, color, type, description, is_system, sort_order, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?)`, c.ID, c.Name, c.Color, c.Type, c.Description, sys, c.SortOrder, c.CreatedAt, c.UpdatedAt)
	return err
}

func (r *Categories) CreateWithID(tx *sql.Tx, c *core.Category) error {
	if err := r.freeName(tx, c.Name, c.ID); err != nil {
		return err
	}
	c.CreatedAt = util.NowStr()
	c.UpdatedAt = c.CreatedAt
	sys := 0
	if c.IsSystem {
		sys = 1
	}
	_, err := exec(r.DB, tx, `INSERT INTO categories (id, name, color, type, description, is_system, sort_order, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?)`, c.ID, c.Name, c.Color, c.Type, c.Description, sys, c.SortOrder, c.CreatedAt, c.UpdatedAt)
	return err
}

func (r *Categories) Update(tx *sql.Tx, c *core.Category) error {
	if err := r.freeName(tx, c.Name, c.ID); err != nil {
		return err
	}
	c.UpdatedAt = util.NowStr()
	sys := 0
	if c.IsSystem {
		sys = 1
	}
	_, err := exec(r.DB, tx, `UPDATE categories SET name=?, color=?, type=?, description=?, is_system=?, updated_at=? WHERE id=? AND deleted_at IS NULL`,
		c.Name, c.Color, c.Type, c.Description, sys, c.UpdatedAt, c.ID)
	return err
}

func (r *Categories) Delete(tx *sql.Tx, id string) error {
	_, err := exec(r.DB, tx, `UPDATE categories SET deleted_at = ? WHERE id = ? AND is_system = 0`, util.NowStr(), id)
	return err
}

// Namesake finds the live category that has taken a deleted category's
// name. It returns the name the deleted one had and the id of the one that
// has it now, "" if the name is free.
func (r *Categories) Namesake(tx *sql.Tx, id string) (name, by string, err error) {
	err = Row(r.DB, tx, `SELECT name FROM categories WHERE id = ? AND deleted_at IS NOT NULL`, id).Scan(&name)
	if err == sql.ErrNoRows {
		return "", "", ErrNotFound
	}
	if err != nil {
		return "", "", err
	}
	name = strings.TrimSuffix(name, setAside(id))
	err = Row(r.DB, tx, `SELECT id FROM categories WHERE name = ? COLLATE NOCASE AND id != ? AND deleted_at IS NULL`,
		name, id).Scan(&by)
	if err == sql.ErrNoRows {
		err = nil
	}
	return name, by, err
}

// Restore brings a deleted category back under the name it had, or returns
// ErrNameTaken if a live category has that name now.
func (r *Categories) Restore(tx *sql.Tx, id string) error {
	name, by, err := r.Namesake(tx, id)
	if err != nil {
		return err
	}
	if by != "" {
		return ErrNameTaken
	}
	if err := r.freeName(tx, name, id); err != nil {
		return err
	}
	_, err = exec(r.DB, tx, `UPDATE categories SET deleted_at = NULL, name = ?, updated_at = ? WHERE id = ?`, name, util.NowStr(), id)
	return err
}

func (r *Categories) NextSortOrder() int {
	var n int
	_ = r.DB.QueryRow(`SELECT COALESCE(MAX(sort_order),0)+1 FROM categories WHERE deleted_at IS NULL`).Scan(&n)
	return n
}
