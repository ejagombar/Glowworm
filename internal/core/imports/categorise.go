package imports

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"glowworm/internal/ai"
	"glowworm/internal/core"
	"glowworm/internal/core/normalize"
)

// Rows sharing a key must always share a category. Direction is part of the key
// because categories are type-masked.
func GroupKey(desc, dest string, amount int64) string {
	d := normalize.Text(desc)
	if d == "" {
		d = strings.ToLower(strings.TrimSpace(desc))
	}
	// Destinations are often account numbers, which normalize.Text would
	// collapse to "#".
	return typeFor(amount) + "|" + d + "|" + strings.Join(strings.Fields(strings.ToLower(dest)), " ")
}

type Progress struct {
	Phase    string `json:"phase"`
	Index    int    `json:"index,omitempty"`
	Total    int    `json:"total,omitempty"`
	GroupKey string `json:"group_key,omitempty"`
	Rows     int    `json:"rows,omitempty"`

	CategoryID  string  `json:"category_id,omitempty"`
	Category    string  `json:"category,omitempty"`
	Source      string  `json:"source,omitempty"`
	Confidence  float64 `json:"confidence,omitempty"`
	NeedsReview bool    `json:"needs_review,omitempty"`

	SuggestedCategoryID string  `json:"suggested_category_id,omitempty"`
	SuggestedCategory   string  `json:"suggested_category,omitempty"`
	SuggestedConfidence float64 `json:"suggested_confidence,omitempty"`

	Categorised int    `json:"categorised,omitempty"`
	Pending     int    `json:"pending,omitempty"`
	Batches     int    `json:"batches,omitempty"`
	Message     string `json:"message,omitempty"`
}

type stagedGroup struct {
	Key         string
	RowIDs      []string
	Type        string
	Description string
	Destination string
	AmountMinor int64
	Date        string
	CategoryID  string
	Source      string
}

func (s *Service) AIAvailable() bool {
	return s.Cat != nil && s.Cat.Provider != nil
}

func (s *Service) Categorise(ctx context.Context, batchID string, emit func(Progress)) error {
	if err := s.CategoriseLocal(ctx, batchID, emit); err != nil {
		return err
	}
	if s.AIAvailable() {
		if err := s.CategoriseAI(ctx, batchID, emit); err != nil {
			return err
		}
	} else {
		emit(Progress{Phase: "ai_skipped", Message: "AI is not configured"})
	}
	groups, err := s.loadGroups(batchID)
	if err != nil {
		return err
	}
	done, pending := countGroups(groups)
	emit(Progress{Phase: "done", Total: len(groups), Categorised: done, Pending: pending})
	return nil
}

func (s *Service) CategoriseLocal(ctx context.Context, batchID string, emit func(Progress)) error {
	if err := s.requireReview(batchID); err != nil {
		return err
	}
	if s.Cat == nil {
		return &ValidationError{Message: "Categoriser is not configured"}
	}
	groups, err := s.loadGroups(batchID)
	if err != nil {
		return err
	}
	emit(Progress{Phase: "local_start", Total: len(groups)})

	rules, err := s.loadRulesTable()
	if err != nil {
		return err
	}
	examples, err := s.loadExampleCounts()
	if err != nil {
		return err
	}
	internalCat, err := s.internalCategoryID()
	if err != nil {
		return err
	}
	cats, err := s.Cat.Categories.List()
	if err != nil {
		return err
	}
	catByID := map[string]*core.Category{}
	for _, c := range cats {
		catByID[c.ID] = c
	}
	localThresh, _, minExamples := s.catThresholds()

	events := make([]Progress, 0, len(groups))
	err = s.DB.WithTx(func(tx *sql.Tx) error {
		for i, g := range groups {
			ev := Progress{Phase: "group", Index: i + 1, Total: len(groups), GroupKey: g.Key, Rows: len(g.RowIDs)}
			if g.Source == core.CategorySourceUser {
				ev.CategoryID, ev.Category, ev.Source = g.CategoryID, nameOf(catByID, g.CategoryID), g.Source
				events = append(events, ev)
				continue
			}
			catID, conf := s.decideLocal(g, rules, examples, catByID, internalCat, localThresh, minExamples)
			if catID == "" {
				ev.NeedsReview = true
				groups[i].CategoryID, groups[i].Source = "", core.CategorySourceNone
				if err := writeGroup(tx, g.RowIDs, "", core.CategorySourceNone, 0); err != nil {
					return err
				}
			} else {
				ev.CategoryID, ev.Category, ev.Source, ev.Confidence = catID, nameOf(catByID, catID), core.CategorySourceLocal, conf
				groups[i].CategoryID, groups[i].Source = catID, core.CategorySourceLocal
				if err := writeGroup(tx, g.RowIDs, catID, core.CategorySourceLocal, conf); err != nil {
					return err
				}
			}
			events = append(events, ev)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, ev := range events {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		emit(ev)
	}
	done, pending := countGroups(groups)
	emit(Progress{Phase: "local_done", Total: len(groups), Categorised: done, Pending: pending})
	return nil
}

const aiChunkSize = 40

func (s *Service) CategoriseAI(ctx context.Context, batchID string, emit func(Progress)) error {
	if !s.AIAvailable() {
		return &ValidationError{Message: "AI provider is not configured"}
	}
	if err := s.requireReview(batchID); err != nil {
		return err
	}
	groups, err := s.loadGroups(batchID)
	if err != nil {
		return err
	}
	var pending []stagedGroup
	for _, g := range groups {
		if g.CategoryID == "" {
			pending = append(pending, g)
		}
	}
	if len(pending) == 0 {
		emit(Progress{Phase: "ai_skipped", Message: "Nothing left for the AI"})
		return nil
	}
	cats, err := s.Cat.Categories.List()
	if err != nil {
		return err
	}
	catalogue := map[string]map[string]string{}
	nameToID := map[string]map[string]string{}
	for _, c := range cats {
		var txType string
		switch c.Type {
		case core.CategorySpend:
			txType = core.TypeExpense
		case core.CategoryIncome:
			txType = core.TypeIncome
		default:
			continue
		}
		if catalogue[txType] == nil {
			catalogue[txType] = map[string]string{}
			nameToID[txType] = map[string]string{}
		}
		catalogue[txType][c.ID] = c.Name
		nameToID[txType][c.Name] = c.ID
	}
	_, aiThresh, _ := s.catThresholds()

	var chunks [][]stagedGroup
	for _, txType := range []string{core.TypeExpense, core.TypeIncome} {
		var cur []stagedGroup
		for _, g := range pending {
			if g.Type != txType {
				continue
			}
			cur = append(cur, g)
			if len(cur) == aiChunkSize {
				chunks = append(chunks, cur)
				cur = nil
			}
		}
		if len(cur) > 0 {
			chunks = append(chunks, cur)
		}
	}
	emit(Progress{Phase: "ai_start", Total: len(pending), Batches: len(chunks)})

	type aiItem struct {
		Index       int    `json:"index"`
		Date        string `json:"date"`
		Description string `json:"description"`
		Destination string `json:"destination,omitempty"`
		AmountMinor int64  `json:"amount_minor"`
		Type        string `json:"type"`
		Count       int    `json:"count"`
	}
	aiCount := 0
	for ci, chunk := range chunks {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		txType := chunk[0].Type
		if len(catalogue[txType]) == 0 {
			emit(Progress{Phase: "warning", Message: fmt.Sprintf("No %s categories exist yet, so %d groups are left for you", txType, len(chunk))})
			emit(Progress{Phase: "ai_progress", Index: ci + 1, Total: len(chunks)})
			continue
		}
		items := make([]aiItem, len(chunk))
		for i, g := range chunk {
			items[i] = aiItem{Index: i, Date: g.Date, Description: g.Description, Destination: g.Destination,
				AmountMinor: g.AmountMinor, Type: g.Type, Count: len(g.RowIDs)}
		}
		resp, _, err := s.Cat.Provider.CompleteStructured(&ai.StructuredRequest{
			Capability: "categorisation",
			PromptJSON: map[string]any{"transactions": items, "allowed_categories": catalogue[txType]},
		})
		var parsed struct {
			Items []struct {
				Index      int     `json:"index"`
				Category   string  `json:"category"`
				Confidence float64 `json:"confidence"`
			} `json:"items"`
		}
		if err == nil {
			err = json.Unmarshal([]byte(resp), &parsed)
		}
		if err != nil {
			// A dead provider would fail every remaining chunk too.
			emit(Progress{Phase: "warning", Message: "The AI request failed (" + err.Error() + "), so the remaining groups are left for you"})
			break
		}
		var events []Progress
		err = s.DB.WithTx(func(tx *sql.Tx) error {
			for _, it := range parsed.Items {
				if it.Index < 0 || it.Index >= len(chunk) {
					continue
				}
				g := chunk[it.Index]
				ev := Progress{Phase: "group", GroupKey: g.Key, Rows: len(g.RowIDs), NeedsReview: true}
				catID := nameToID[txType][it.Category]
				switch {
				case catID == "":
					ev.Message = "The AI couldn't place this"
				case it.Confidence >= aiThresh:
					if err := writeGroup(tx, g.RowIDs, catID, core.CategorySourceAI, it.Confidence); err != nil {
						return err
					}
					ev.NeedsReview = false
					ev.CategoryID, ev.Category, ev.Source, ev.Confidence = catID, it.Category, core.CategorySourceAI, it.Confidence
					aiCount++
				default:
					if err := writeSuggestion(tx, g.RowIDs, catID, it.Confidence); err != nil {
						return err
					}
					ev.SuggestedCategoryID, ev.SuggestedCategory, ev.SuggestedConfidence = catID, it.Category, it.Confidence
				}
				events = append(events, ev)
			}
			return nil
		})
		if err != nil {
			return err
		}
		for _, ev := range events {
			emit(ev)
		}
		emit(Progress{Phase: "ai_progress", Index: ci + 1, Total: len(chunks)})
	}
	emit(Progress{Phase: "ai_done", Total: len(pending), Categorised: aiCount, Pending: len(pending) - aiCount})
	return nil
}

func (s *Service) requireReview(batchID string) error {
	batch, err := s.getBatch(batchID)
	if err != nil {
		return err
	}
	if batch.Status != "awaiting_review" {
		return &ValidationError{Message: "This import is not ready for categorisation"}
	}
	return nil
}

func countGroups(groups []stagedGroup) (categorised, pending int) {
	for _, g := range groups {
		if g.CategoryID != "" {
			categorised++
		} else {
			pending++
		}
	}
	return categorised, pending
}

func nameOf(catByID map[string]*core.Category, id string) string {
	if c, ok := catByID[id]; ok {
		return c.Name
	}
	return ""
}

func (s *Service) decideLocal(
	g stagedGroup,
	rules map[string]string,
	examples map[string]int,
	catByID map[string]*core.Category,
	internalCat string,
	localThresh float64,
	minExamples int,
) (categoryID string, confidence float64) {
	if g.Type == core.TypeTransfer && internalCat != "" {
		return internalCat, 1.0
	}
	allowed := map[string]bool{}
	for id, cat := range catByID {
		switch g.Type {
		case core.TypeExpense:
			if cat.Type == core.CategorySpend {
				allowed[id] = true
			}
		case core.TypeIncome:
			if cat.Type == core.CategoryIncome {
				allowed[id] = true
			}
		}
	}
	key := normalize.Text(g.Description)
	if catID, ok := rules[key]; ok && allowed[catID] {
		return catID, 1.0
	}
	if s.Cat != nil && s.Cat.Model != nil {
		preds := s.Cat.Model.Predict(key, allowed)
		if len(preds) > 0 && examples[preds[0].CategoryID] >= minExamples && preds[0].Confidence >= localThresh {
			return preds[0].CategoryID, preds[0].Confidence
		}
	}
	return "", 0
}

func (s *Service) loadGroups(batchID string) ([]stagedGroup, error) {
	rows, err := s.DB.Query(`SELECT id, date, description, destination_text, amount_minor,
		COALESCE(proposed_category_id, ''), category_source
		FROM import_staging WHERE batch_id = ? AND duplicate_of IS NULL ORDER BY line_no ASC`, batchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []stagedGroup
	index := map[string]int{}
	for rows.Next() {
		var id, date, desc, dest, catID, source string
		var amount int64
		if err := rows.Scan(&id, &date, &desc, &dest, &amount, &catID, &source); err != nil {
			return nil, err
		}
		key := GroupKey(desc, dest, amount)
		i, ok := index[key]
		if !ok {
			i = len(out)
			index[key] = i
			out = append(out, stagedGroup{Key: key, Type: typeFor(amount), Description: desc,
				Destination: dest, AmountMinor: amount, Date: date, CategoryID: catID, Source: source})
		}
		out[i].RowIDs = append(out[i].RowIDs, id)
		if source == core.CategorySourceUser && out[i].Source != core.CategorySourceUser {
			out[i].CategoryID, out[i].Source = catID, source
		}
	}
	return out, rows.Err()
}

func writeGroup(tx *sql.Tx, rowIDs []string, categoryID, source string, confidence float64) error {
	var catID, conf any
	needs := 1
	if categoryID != "" {
		catID, needs = categoryID, 0
		if confidence > 0 {
			conf = confidence
		}
	}
	for _, id := range rowIDs {
		if _, err := tx.Exec(`UPDATE import_staging SET proposed_category_id = ?, category_source = ?,
			category_confidence = ?, needs_review = ?, suggested_category_id = NULL, suggested_confidence = NULL
			WHERE id = ? AND category_source != 'user'`, catID, source, conf, needs, id); err != nil {
			return err
		}
	}
	return nil
}

func writeSuggestion(tx *sql.Tx, rowIDs []string, categoryID string, confidence float64) error {
	for _, id := range rowIDs {
		if _, err := tx.Exec(`UPDATE import_staging SET proposed_category_id = NULL, category_source = 'none',
			category_confidence = NULL, needs_review = 1, suggested_category_id = ?, suggested_confidence = ?
			WHERE id = ? AND category_source != 'user'`, categoryID, confidence, id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) loadRulesTable() (map[string]string, error) {
	rows, err := s.DB.Query(`SELECT pattern, category_id FROM category_rules`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var p, c string
		if err := rows.Scan(&p, &c); err != nil {
			return nil, err
		}
		out[p] = c
	}
	return out, rows.Err()
}

func (s *Service) loadExampleCounts() (map[string]int, error) {
	rows, err := s.DB.Query(`SELECT category_id, COUNT(*) FROM transactions
		WHERE deleted_at IS NULL AND category_source = 'user' AND category_id IS NOT NULL GROUP BY category_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

func (s *Service) internalCategoryID() (string, error) {
	var id string
	err := s.DB.QueryRow(`SELECT id FROM categories WHERE name = 'Internal transfer' COLLATE NOCASE AND deleted_at IS NULL`).Scan(&id)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("internal category lookup: %w", err)
	}
	return id, nil
}

func (s *Service) catThresholds() (local, aiT float64, minExamples int) {
	if s.Cat == nil || s.Cat.Settings == nil {
		return 0.75, 0.8, 3
	}
	return s.Cat.Settings.GetFloat("cat_local_threshold", 0.75),
		s.Cat.Settings.GetFloat("cat_ai_threshold", 0.8),
		int(s.Cat.Settings.GetFloat("cat_min_examples", 3))
}
