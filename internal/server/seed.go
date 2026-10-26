package server

import (
	"database/sql"
	"strings"

	"glowworm/internal/core"
	"glowworm/internal/core/audit"
	"glowworm/internal/util"
)

const keyExamplesSeeded = "examples_seeded"

var exampleCategories = []struct{ Name, Color string }{
	{"Transport", "#4f5bc7"}, {"Food & Dining", "#c9822e"}, {"Bills & Utilities", "#c0483f"},
	{"Entertainment", "#7a58b8"}, {"Shopping", "#2f7fb0"}, {"Health", "#0e7a4d"},
}

// SeedExamples gives a fresh instance example categories and dashboard
// widgets, each logged as one System entry. It runs once; anything the user
// already made is left alone.
func (a *App) SeedExamples() error {
	if a.Repos.Settings.GetBool(keyExamplesSeeded) {
		return nil
	}
	return a.DB.WithTx(func(tx *sql.Tx) error {
		if err := a.seedExampleCategories(tx); err != nil {
			return err
		}
		if err := a.seedExampleWidgets(tx); err != nil {
			return err
		}
		return a.Repos.Settings.Set(tx, keyExamplesSeeded, true)
	})
}

func (a *App) seedExampleCategories(tx *sql.Tx) error {
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM categories WHERE deleted_at IS NULL AND is_system = 0`).Scan(&n); err != nil || n > 0 {
		return err
	}
	var names []string
	var diff []audit.DiffLine
	for i, ex := range exampleCategories {
		c := &core.Category{Name: ex.Name, Color: ex.Color, Type: core.CategorySpend, SortOrder: i + 1}
		if err := a.Repos.Categories.Create(tx, c); err != nil {
			return err
		}
		names = append(names, c.Name)
		diff = append(diff, audit.Add(c.Name))
	}
	_, err := a.AuditWriter.Write(tx, audit.Input{
		Actor: core.ActorSystem, Action: "setup.categories_seeded",
		EntityType: "category", Title: "Added example categories",
		Sub: strings.Join(names, ", "), Diff: diff,
	})
	return err
}

func (a *App) seedExampleWidgets(tx *sql.Tx) error {
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM pinned_widgets`).Scan(&n); err != nil || n > 0 {
		return err
	}
	seeds := []struct {
		title, typ, plan string
		col, rows        int
	}{
		{"Spending by category — this month", "bar", seedPlanThisMonth("spend", "category"), 8, 4},
		{"Savings rate", "number", seedPlanNumber(), 4, 2},
		{"Spending mix — this month", "pie", seedPlanThisMonth("spend", "category"), 4, 3},
		{"Top merchants", "table", seedPlanTopMerchants(), 8, 4},
	}
	var diff []audit.DiffLine
	for i, s := range seeds {
		created := util.NowStr()
		if _, err := tx.Exec(`INSERT INTO pinned_widgets (id, position, col, rows, title, type, dataset, plan, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?)`,
			util.NewID(), i, s.col, s.rows, s.title, s.typ, "{}", s.plan, created, created); err != nil {
			return err
		}
		diff = append(diff, audit.Add(s.title))
	}
	_, err := a.AuditWriter.Write(tx, audit.Input{
		Actor: core.ActorSystem, Action: "setup.widgets_seeded",
		EntityType: "widget", Title: "Pinned example widgets to the dashboard",
		Sub: "Unpin any you don't want", Diff: diff,
	})
	return err
}
