// Package goals keeps savings goals: a target amount, by a date or not,
// over some accounts (every savings account when it names none). How a
// goal is going is worked out by ledger.
package goals

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ejagombar/Glowworm/internal/core"
	"github.com/ejagombar/Glowworm/internal/core/audit"
	"github.com/ejagombar/Glowworm/internal/money"
	"github.com/ejagombar/Glowworm/internal/store"
	"github.com/ejagombar/Glowworm/internal/util"
)

type Service struct {
	DB    *store.DB
	Audit *audit.Writer
	// Currency is the display currency, for the history entries.
	Currency func() string
}

// Goal is a goal as stored.
type Goal struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	TargetMinor int64    `json:"target_minor"`
	TargetDate  string   `json:"target_date"`
	Color       string   `json:"color"`
	Notes       string   `json:"notes"`
	AccountIDs  []string `json:"account_ids"`
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`
}

// Input is a goal as a form sends it.
type Input struct {
	Name        string   `json:"name"`
	TargetMinor int64    `json:"target_minor"`
	TargetDate  string   `json:"target_date"`
	Color       string   `json:"color"`
	Notes       string   `json:"notes"`
	AccountIDs  []string `json:"account_ids"`
}

var ErrNotFound = errors.New("goal not found")

type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

func (s *Service) validate(in *Input) error {
	in.Name = strings.TrimSpace(in.Name)
	in.TargetDate = strings.TrimSpace(in.TargetDate)
	in.Notes = strings.TrimSpace(in.Notes)
	switch {
	case in.Name == "":
		return &ValidationError{"Give the goal a name"}
	case len(in.Name) > 80:
		return &ValidationError{"The goal's name is too long"}
	case in.TargetMinor <= 0:
		return &ValidationError{"The target must be more than zero"}
	case len(in.Notes) > 1000:
		return &ValidationError{"The notes are too long"}
	}
	if in.TargetDate != "" {
		if _, err := time.Parse("2006-01-02", in.TargetDate); err != nil {
			return &ValidationError{"The target date must be YYYY-MM-DD"}
		}
	}
	if in.Color == "" {
		in.Color = "#0e6f51"
	}
	if !core.ValidColor(in.Color) {
		return &ValidationError{"Colour must be a hex code like #2e9d8f"}
	}
	seen := map[string]bool{}
	var ids []string
	for _, id := range in.AccountIDs {
		if id = strings.TrimSpace(id); id == "" || seen[id] {
			continue
		}
		var n int
		if err := s.DB.QueryRow(`SELECT COUNT(*) FROM accounts WHERE id = ? AND deleted_at IS NULL`, id).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return &ValidationError{"One of the goal's accounts no longer exists"}
		}
		seen[id] = true
		ids = append(ids, id)
	}
	in.AccountIDs = ids
	return nil
}

// List is every goal, oldest first.
func (s *Service) List() ([]*Goal, error) {
	rows, err := s.DB.Query(`SELECT id, name, target_minor, COALESCE(target_date, ''), color, notes, created_at, updated_at
		FROM goals WHERE deleted_at IS NULL ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	var out []*Goal
	byID := map[string]*Goal{}
	for rows.Next() {
		g := &Goal{AccountIDs: []string{}}
		if err := rows.Scan(&g.ID, &g.Name, &g.TargetMinor, &g.TargetDate, &g.Color, &g.Notes, &g.CreatedAt, &g.UpdatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, g)
		byID[g.ID] = g
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = s.DB.Query(`SELECT ga.goal_id, ga.account_id FROM goal_accounts ga
		JOIN accounts a ON a.id = ga.account_id AND a.deleted_at IS NULL ORDER BY a.created_at, a.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var gid, aid string
		if err := rows.Scan(&gid, &aid); err != nil {
			return nil, err
		}
		if g := byID[gid]; g != nil {
			g.AccountIDs = append(g.AccountIDs, aid)
		}
	}
	return out, rows.Err()
}

// Get is one goal.
func (s *Service) Get(id string) (*Goal, error) {
	list, err := s.List()
	if err != nil {
		return nil, err
	}
	for _, g := range list {
		if g.ID == id {
			return g, nil
		}
	}
	return nil, ErrNotFound
}

func (s *Service) describe(in Input) string {
	cur := "GBP"
	if s.Currency != nil && s.Currency() != "" {
		cur = s.Currency()
	}
	sub := in.Name + " · " + money.Format(in.TargetMinor, cur)
	if in.TargetDate != "" {
		sub += " by " + in.TargetDate
	}
	return strings.TrimSpace(sub)
}

func (s *Service) Create(in Input) (*Goal, error) {
	if err := s.validate(&in); err != nil {
		return nil, err
	}
	id := util.NewID()
	now := util.NowStr()
	err := s.DB.WithTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO goals (id, name, target_minor, target_date, color, notes, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?)`, id, in.Name, in.TargetMinor, nullIfEmpty(in.TargetDate), in.Color, in.Notes, now, now); err != nil {
			return err
		}
		if err := setAccounts(tx, id, in.AccountIDs); err != nil {
			return err
		}
		return s.record(tx, "goal.created", id, "Added goal", s.describe(in), in)
	})
	if err != nil {
		return nil, err
	}
	return s.Get(id)
}

func (s *Service) Update(id string, in Input) (*Goal, error) {
	if err := s.validate(&in); err != nil {
		return nil, err
	}
	if _, err := s.Get(id); err != nil {
		return nil, err
	}
	err := s.DB.WithTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE goals SET name=?, target_minor=?, target_date=?, color=?, notes=?, updated_at=?
			WHERE id=? AND deleted_at IS NULL`, in.Name, in.TargetMinor, nullIfEmpty(in.TargetDate), in.Color, in.Notes,
			util.NowStr(), id); err != nil {
			return err
		}
		if err := setAccounts(tx, id, in.AccountIDs); err != nil {
			return err
		}
		return s.record(tx, "goal.updated", id, "Updated goal", s.describe(in), in)
	})
	if err != nil {
		return nil, err
	}
	return s.Get(id)
}

func (s *Service) Delete(id string) error {
	g, err := s.Get(id)
	if err != nil {
		return err
	}
	return s.DB.WithTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE goals SET deleted_at = ? WHERE id = ?`, util.NowStr(), id); err != nil {
			return err
		}
		return s.record(tx, "goal.deleted", id, "Removed goal", g.Name, nil)
	})
}

func setAccounts(tx *sql.Tx, id string, ids []string) error {
	if _, err := tx.Exec(`DELETE FROM goal_accounts WHERE goal_id = ?`, id); err != nil {
		return err
	}
	for _, aid := range ids {
		if _, err := tx.Exec(`INSERT INTO goal_accounts (goal_id, account_id) VALUES (?, ?)`, id, aid); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) record(tx *sql.Tx, action, id, title, sub string, after any) error {
	if s.Audit == nil {
		return nil
	}
	_, err := s.Audit.Write(tx, audit.Input{
		Actor: core.ActorUser, Action: action, EntityType: "goal", EntityID: id,
		Title: title, Sub: sub, After: after,
	})
	if err != nil {
		return fmt.Errorf("goal history: %w", err)
	}
	return nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
