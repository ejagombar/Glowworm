// Package categories implements the category service (doc 02 §4, doc 03 §6):
// CRUD with system-category rules and audit.
package categories

import (
	"database/sql"
	"fmt"
	"strings"

	"glowworm/internal/core"
	"glowworm/internal/core/audit"
	"glowworm/internal/store"
	"glowworm/internal/store/repos"
)

// Service owns category rules.
type Service struct {
	DB    *store.DB
	Repos *repos.Categories
	Txs   *repos.Transactions
	Audit *audit.Writer
}

// ValidationError is user-facing.
type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

// Input is create/update payload.
type Input struct {
	Name  string `json:"name"`
	Color string `json:"color"`
	Type  string `json:"type"`
}

// List returns live categories with per-period spend aggregates (doc 03 §6).
func (s *Service) List(from, to string) ([]*core.Category, error) {
	cats, err := s.Repos.List()
	if err != nil {
		return nil, err
	}
	spent := map[string]int64{}
	if from != "" || to != "" {
		q := `SELECT category_id, SUM(amount_minor) FROM transactions
			WHERE deleted_at IS NULL AND type = 'expense' AND category_id IS NOT NULL`
		args := []any{}
		if from != "" {
			q += ` AND date >= ?`
			args = append(args, from)
		}
		if to != "" {
			q += ` AND date <= ?`
			args = append(args, to)
		}
		q += ` GROUP BY category_id`
		rows, err := s.DB.Query(q, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			var sum int64
			if err := rows.Scan(&id, &sum); err != nil {
				rows.Close()
				return nil, err
			}
			spent[id] = sum
		}
		rows.Close()
	}
	for _, c := range cats {
		if c.Type == core.CategorySpend {
			v := spent[c.ID]
			c.SpentMinor = &v
		}
	}
	return cats, nil
}

// Create adds a user category (audit category.created).
func (s *Service) Create(in Input) (*core.Category, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, &ValidationError{Message: "Category name is required"}
	}
	if len(name) > 60 {
		return nil, &ValidationError{Message: "Category name is too long"}
	}
	if clash, _ := s.Repos.ByName(name); clash != nil {
		return nil, &ValidationError{Message: "A category with that name already exists"}
	}
	typ := in.Type
	if typ == "" {
		typ = core.CategorySpend
	}
	if typ != core.CategorySpend && typ != core.CategoryIncome {
		return nil, &ValidationError{Message: "Category type must be spend or income"}
	}
	c := &core.Category{Name: name, Color: in.Color, Type: typ, IsSystem: false,
		SortOrder: s.Repos.NextSortOrder()}
	if c.Color == "" {
		c.Color = core.Palette[len(core.Palette)-2]
	}
	err := s.DB.WithTx(func(tx *sql.Tx) error {
		if err := s.Repos.Create(tx, c); err != nil {
			return err
		}
		_, err := s.Audit.Write(tx, audit.Input{
			Actor: core.ActorUser, Action: "category.created",
			EntityType: "category", EntityID: c.ID,
			Title: "Added category", Sub: name,
			After:  map[string]any{"id": c.ID, "name": c.Name, "color": c.Color, "type": c.Type},
			Revert: map[string]any{"category_id": c.ID},
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	return c, nil
}

// Update renames/recolors (audit category.updated). System categories may
// only change colour [D11].
func (s *Service) Update(id string, in Input) (*core.Category, error) {
	c, err := s.Repos.Get(id)
	if err != nil {
		return nil, err
	}
	before := map[string]any{"name": c.Name, "color": c.Color, "type": c.Type}
	if c.IsSystem {
		// Name and type fixed; colour only.
		if strings.TrimSpace(in.Name) != "" && !strings.EqualFold(in.Name, c.Name) {
			return nil, &ValidationError{Message: "System category — its name is fixed."}
		}
		if in.Color != "" {
			c.Color = in.Color
		}
	} else {
		name := strings.TrimSpace(in.Name)
		if name == "" {
			return nil, &ValidationError{Message: "Category name is required"}
		}
		if !strings.EqualFold(name, c.Name) {
			if clash, _ := s.Repos.ByName(name); clash != nil && clash.ID != c.ID {
				return nil, &ValidationError{Message: "A category with that name already exists"}
			}
		}
		c.Name = name
		if in.Color != "" {
			c.Color = in.Color
		}
	}
	err = s.DB.WithTx(func(tx *sql.Tx) error {
		if err := s.Repos.Update(tx, c); err != nil {
			return err
		}
		after := map[string]any{"name": c.Name, "color": c.Color, "type": c.Type}
		diff := []audit.DiffLine{}
		if before["name"] != after["name"] {
			diff = append(diff, audit.DiffLine{Line: "− " + fmt.Sprint(before["name"]), Cls: "diff-del"},
				audit.DiffLine{Line: "+ " + fmt.Sprint(after["name"]), Cls: "diff-add"})
		}
		title := "Updated category"
		sub := c.Name
		if c.IsSystem {
			title = "Colour updated"
		}
		_, err := s.Audit.Write(tx, audit.Input{
			Actor: core.ActorUser, Action: "category.updated",
			EntityType: "category", EntityID: c.ID,
			Title: title, Sub: sub, Diff: diff,
			Before: before, After: after,
			Revert: map[string]any{"category_id": c.ID, "fields": before},
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.Repos.Get(id)
}

// Delete soft-deletes a user category (audit category.deleted). Transactions
// keep pointing at the soft-deleted row; they render as Uncategorised
// (doc 02 §4).
func (s *Service) Delete(id string) error {
	c, err := s.Repos.Get(id)
	if err != nil {
		return err
	}
	if c.IsSystem {
		return &ValidationError{Message: "System categories can't be deleted"}
	}
	var affected int64
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM transactions WHERE category_id = ? AND deleted_at IS NULL`, id).Scan(&affected)
	return s.DB.WithTx(func(tx *sql.Tx) error {
		if err := s.Repos.Delete(tx, id); err != nil {
			return err
		}
		_, err := s.Audit.Write(tx, audit.Input{
			Actor: core.ActorUser, Action: "category.deleted",
			EntityType: "category", EntityID: id,
			Title: "Deleted category", Sub: fmt.Sprintf("%s · %d transactions now uncategorised", c.Name, affected),
			Before: map[string]any{"id": c.ID, "name": c.Name, "color": c.Color, "type": c.Type},
			Revert: map[string]any{"category_id": c.ID, "restore": true},
		})
		return err
	})
}
