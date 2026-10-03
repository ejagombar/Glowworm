// Package categoriser implements the four-tier cascade (doc 07):
// rules → local classifier → AI fallback → human review.
package categoriser

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"glowworm/internal/ai"
	"glowworm/internal/classifier"
	"glowworm/internal/core"
	"glowworm/internal/core/audit"
	"glowworm/internal/core/normalize"
	"glowworm/internal/store"
	"glowworm/internal/store/repos"
)

// Service runs the cascade.
type Service struct {
	DB         *store.DB
	Txs        *repos.Transactions
	Categories *repos.Categories
	Settings   *repos.Settings
	Audit      *audit.Writer
	Provider   ai.Provider // nil = AI disabled
	Model      *classifier.Model
}

// Result summarises one categorisation run.
type Result struct {
	Rules    int
	Local    int
	AI       int
	Review   int
	Assigned []Assignment
}

// Assignment is one row outcome (audit diff lines).
type Assignment struct {
	TxID       string
	CategoryID string
	Category   string
	Source     string
}

// Thresholds from settings (doc 07 §2).
func (s *Service) thresholds() (local, ai float64, minExamples int) {
	return s.Settings.GetFloat("cat_local_threshold", 0.75),
		s.Settings.GetFloat("cat_ai_threshold", 0.8),
		int(s.Settings.GetFloat("cat_min_examples", 3))
}

// eligibleCategories returns live categories keyed by id, filtered by the
// transaction type (doc 07 §1 masking).
func (s *Service) eligibleCategories(txType string) (map[string]*core.Category, error) {
	cats, err := s.Categories.List()
	if err != nil {
		return nil, err
	}
	out := map[string]*core.Category{}
	for _, c := range cats {
		switch txType {
		case core.TypeExpense:
			if c.Type == core.CategorySpend {
				out[c.ID] = c
			}
		case core.TypeIncome:
			if c.Type == core.CategoryIncome {
				out[c.ID] = c
			}
		}
	}
	return out, nil
}

// Categorise runs the cascade for the given transactions (in a tx).
func (s *Service) Categorise(tx *sql.Tx, txs []*core.Transaction) (*Result, error) {
	res := &Result{}
	internalCat, err := s.internalCategoryID(tx)
	if err != nil {
		return nil, err
	}
	localThresh, aiThresh, minExamples := s.thresholds()

	// Training-example counts per category (guard, doc 07 §2).
	examples := map[string]int{}
	rows, err := tx.Query(`SELECT category_id, COUNT(*) FROM transactions
		WHERE deleted_at IS NULL AND category_source = 'user' AND category_id IS NOT NULL GROUP BY category_id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var n int
		_ = rows.Scan(&id, &n)
		examples[id] = n
	}
	rows.Close()

	// Rules tier table (doc 07 §4).
	rules := map[string]string{}
	rrows, err := tx.Query(`SELECT pattern, category_id FROM category_rules`)
	if err != nil {
		return nil, err
	}
	for rrows.Next() {
		var p, c string
		_ = rrows.Scan(&p, &c)
		rules[p] = c
	}
	rrows.Close()

	var needAI []*core.Transaction

	for _, t := range txs {
		if t.Type == core.TypeTransfer {
			if err := assign(tx, t, internalCat, core.CategorySourceLocal, 1.0); err != nil {
				return nil, err
			}
			res.Local++
			res.Assigned = append(res.Assigned, Assignment{TxID: t.ID, CategoryID: internalCat, Category: "Internal transfer", Source: "local"})
			continue
		}
		candidates, err := s.eligibleCategories(t.Type)
		if err != nil {
			return nil, err
		}
		allowedIDs := map[string]bool{}
		for id := range candidates {
			allowedIDs[id] = true
		}
		key := normalize.Text(t.Description)

		// Tier 1: rules.
		if catID, ok := rules[key]; ok && allowedIDs[catID] {
			if err := assign(tx, t, catID, core.CategorySourceLocal, 1.0); err != nil {
				return nil, err
			}
			res.Rules++
			res.Assigned = append(res.Assigned, Assignment{TxID: t.ID, CategoryID: catID, Category: candidates[catID].Name, Source: "local"})
			continue
		}

		// Tier 2: local classifier.
		if s.Model != nil {
			preds := s.Model.Predict(key, allowedIDs)
			if len(preds) > 0 && examples[preds[0].CategoryID] >= minExamples && preds[0].Confidence >= localThresh {
				if err := assign(tx, t, preds[0].CategoryID, core.CategorySourceLocal, preds[0].Confidence); err != nil {
					return nil, err
				}
				res.Local++
				res.Assigned = append(res.Assigned, Assignment{TxID: t.ID, CategoryID: preds[0].CategoryID, Category: candidates[preds[0].CategoryID].Name, Source: "local"})
				continue
			}
		}

		// Tier 3: AI fallback (batched later).
		if s.Provider != nil && len(candidates) > 0 {
			needAI = append(needAI, t)
			continue
		}

		// Tier 4: review.
		if err := assign(tx, t, "", core.CategorySourceNone, 0); err != nil {
			return nil, err
		}
		res.Review++
	}

	if len(needAI) > 0 && s.Provider != nil {
		assigned, err := s.aiBatch(tx, needAI, aiThresh)
		if err != nil {
			for _, t := range needAI { // degrade gracefully (doc 09 §9)
				_ = updateCategory(tx, t.ID, "", core.CategorySourceNone, 0)
				res.Review++
			}
		} else {
			res.AI = len(assigned)
			res.Assigned = append(res.Assigned, assigned...)
			res.Review += len(needAI) - len(assigned)
		}
	}
	return res, nil
}

// aiBatch sends one structured-output batch per type group (doc 09 §6.2).
func (s *Service) aiBatch(tx *sql.Tx, txs []*core.Transaction, aiThresh float64) ([]Assignment, error) {
	type item struct {
		Index       int    `json:"index"`
		Date        string `json:"date"`
		Description string `json:"description"`
		AmountMinor int64  `json:"amount_minor"`
		Type        string `json:"type"`
	}
	catsByType := map[string]map[string]string{}
	for _, t := range txs {
		if _, ok := catsByType[t.Type]; ok {
			continue
		}
		cats, err := s.eligibleCategories(t.Type)
		if err != nil {
			return nil, err
		}
		m := map[string]string{}
		for id, c := range cats {
			m[id] = c.Name
		}
		catsByType[t.Type] = m
	}
	assigned := []Assignment{}
	for txType, catalogue := range catsByType {
		var items []item
		idxTx := map[int]string{}
		for _, t := range txs {
			if t.Type != txType {
				continue
			}
			items = append(items, item{Index: len(items), Date: t.Date, Description: t.Description, AmountMinor: t.AmountMinor, Type: t.Type})
			idxTx[len(items)-1] = t.ID
		}
		if len(items) == 0 {
			continue
		}
		resp, _, err := s.Provider.CompleteStructured(&ai.StructuredRequest{
			Capability: "categorisation",
			PromptJSON: map[string]any{
				"transactions":       items,
				"allowed_categories": catalogue,
			},
		})
		if err != nil {
			return nil, err
		}
		var parsed struct {
			Items []struct {
				Index      int     `json:"index"`
				Category   string  `json:"category"`
				Confidence float64 `json:"confidence"`
			} `json:"items"`
		}
		if err := json.Unmarshal([]byte(resp), &parsed); err != nil {
			return nil, err
		}
		nameToID := map[string]string{}
		for id, name := range catalogue {
			nameToID[name] = id
		}
		for _, it := range parsed.Items {
			txID, ok := idxTx[it.Index]
			if !ok || it.Confidence < aiThresh {
				continue
			}
			catID, ok := nameToID[it.Category]
			if !ok {
				continue
			}
			for _, t := range txs {
				if t.ID == txID {
					if err := assign(tx, t, catID, core.CategorySourceAI, it.Confidence); err != nil {
						return nil, err
					}
					break
				}
			}
			assigned = append(assigned, Assignment{TxID: txID, CategoryID: catID, Category: it.Category, Source: "ai"})
		}
	}
	return assigned, nil
}

// updateCategory writes the DB row AND the in-memory struct (the staging
// flow re-reads the struct after the cascade, doc 06 §6).
func updateCategory(tx *sql.Tx, txID, categoryID, source string, confidence float64) error {
	var catID any
	if categoryID != "" {
		catID = categoryID
	}
	var conf any
	if confidence > 0 && categoryID != "" {
		conf = confidence
	}
	_, err := tx.Exec(`UPDATE transactions SET category_id = ?, category_source = ?, category_confidence = ?, updated_at = ?
		WHERE id = ?`, catID, source, conf, utilNow(), txID)
	return err
}

// assign updates a transaction's category in the DB and memory.
func assign(tx *sql.Tx, t *core.Transaction, categoryID, source string, confidence float64) error {
	if err := updateCategory(tx, t.ID, categoryID, source, confidence); err != nil {
		return err
	}
	if categoryID != "" {
		c := categoryID
		t.CategoryID = &c
	} else {
		t.CategoryID = nil
	}
	t.CategorySource = source
	if confidence > 0 && categoryID != "" {
		t.CategoryConfidence = &confidence
	} else {
		t.CategoryConfidence = nil
	}
	return nil
}

func (s *Service) internalCategoryID(tx *sql.Tx) (string, error) {
	var id string
	err := tx.QueryRow(`SELECT id FROM categories WHERE name = 'Internal transfer' COLLATE NOCASE AND deleted_at IS NULL`).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("system category missing: %w", err)
	}
	return id, nil
}

func utilNow() string { return time.Now().UTC().Format(time.RFC3339) }

// RefreshRules rebuilds the rules table from user-confirmed history
// (doc 07 §4): pattern → majority category (≥2 votes, ≥60%).
func (s *Service) RefreshRules() error {
	return s.DB.WithTx(func(tx *sql.Tx) error {
		// Delete stale rules then rebuild from history.
		if _, err := tx.Exec(`DELETE FROM category_rules`); err != nil {
			return err
		}
		rows, err := tx.Query(`SELECT pattern, category_id, COUNT(*) AS votes FROM (
			SELECT ? AS pattern, category_id, 0 AS votes FROM transactions LIMIT 0)`, "")
		if err != nil {
			return err
		}
		rows.Close()
		// Real aggregation: group by normalised description.
		agg := map[string]map[string]int{}
		rows2, err := tx.Query(`SELECT description, category_id FROM transactions
			WHERE deleted_at IS NULL AND category_source = 'user' AND category_id IS NOT NULL`)
		if err != nil {
			return err
		}
		defer rows2.Close()
		for rows2.Next() {
			var desc, catID string
			if err := rows2.Scan(&desc, &catID); err != nil {
				return err
			}
			pattern := normalize.Text(desc)
			if agg[pattern] == nil {
				agg[pattern] = map[string]int{}
			}
			agg[pattern][catID]++
		}
		rows2.Close()
		for pattern, votes := range agg {
			total := 0
			best, bestN := "", 0
			for catID, n := range votes {
				total += n
				if n > bestN {
					best, bestN = catID, n
				}
			}
			if total >= 2 && float64(bestN)/float64(total) >= 0.6 {
				if _, err := tx.Exec(`INSERT OR REPLACE INTO category_rules (pattern, category_id, votes, updated_at)
					VALUES (?,?,?,?)`, pattern, best, bestN, utilNow()); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// TrainFromUserRows builds the local model from category_source='user' rows
// and persists a new version (doc 07 §5.3).
func (s *Service) TrainFromUserRows() (*classifier.Model, error) {
	rows, err := s.DB.Query(`SELECT description, COALESCE(merchant,''), category_id FROM transactions
		WHERE deleted_at IS NULL AND category_source = 'user' AND category_id IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	samples := []classifier.Sample{}
	for rows.Next() {
		var desc, merchant, catID string
		if err := rows.Scan(&desc, &merchant, &catID); err != nil {
			return nil, err
		}
		text := desc
		if merchant != "" && merchant != desc {
			text = desc + " " + merchant
		}
		samples = append(samples, classifier.Sample{Text: normalize.Text(text), CategoryID: catID})
	}
	if len(samples) == 0 {
		return nil, fmt.Errorf("categoriser: no training data")
	}
	var version int
	_ = s.DB.QueryRow(`SELECT COALESCE(MAX(version),0)+1 FROM classifier_models`).Scan(&version)
	model, err := classifier.Train(samples, version, utilNow())
	if err != nil {
		return nil, err
	}
	payload, err := model.Encode()
	if err != nil {
		return nil, err
	}
	_, err = s.DB.Exec(`INSERT INTO classifier_models (version, trained_at, training_count, payload, metrics)
		VALUES (?,?,?,?,?)`, version, utilNow(), len(samples), payload, "{}")
	if err != nil {
		return nil, err
	}
	return model, nil
}

// LoadLatestModel reads the newest persisted model (nil when none).
func (s *Service) LoadLatestModel() (*classifier.Model, error) {
	var payload string
	err := s.DB.QueryRow(`SELECT payload FROM classifier_models ORDER BY version DESC LIMIT 1`).Scan(&payload)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return classifier.DecodeModel(payload)
}

// Retrain refits the model and audits it (doc 07 §5.3).
func (s *Service) Retrain() (*classifier.Model, error) {
	model, err := s.TrainFromUserRows()
	if err != nil {
		return nil, err
	}
	s.Model = model
	err = s.DB.WithTx(func(tx *sql.Tx) error {
		_, err := s.Audit.Write(tx, audit.Input{
			Actor: core.ActorSystem, Action: "classifier.retrained",
			EntityType: "classifier", EntityID: fmt.Sprint(model.Version),
			Title: fmt.Sprintf("Classifier retrained (v%d)", model.Version),
			Sub:   fmt.Sprintf("%d training examples", model.TrainingCount),
		})
		return err
	})
	return model, err
}