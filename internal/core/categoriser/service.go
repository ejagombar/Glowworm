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

type Service struct {
	DB         *store.DB
	Txs        *repos.Transactions
	Categories *repos.Categories
	Settings   *repos.Settings
	Audit      *audit.Writer
	Provider   ai.Provider
	Model      *classifier.Model
}

type Result struct {
	Rules    int
	Local    int
	AI       int
	Review   int
	Assigned []Assignment
}

type Assignment struct {
	TxID       string
	CategoryID string
	Category   string
	Source     string
}

func (s *Service) thresholds() (local, ai float64, minExamples int) {
	return s.Settings.GetFloat("cat_local_threshold", 0.75),
		s.Settings.GetFloat("cat_ai_threshold", 0.8),
		int(s.Settings.GetFloat("cat_min_examples", 3))
}

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

func (s *Service) Categorise(tx *sql.Tx, txs []*core.Transaction) (*Result, error) {
	res := &Result{}
	internalCat, err := s.internalCategoryID(tx)
	if err != nil {
		return nil, err
	}
	localThresh, aiThresh, minExamples := s.thresholds()

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

		if catID, ok := rules[key]; ok && allowedIDs[catID] {
			if err := assign(tx, t, catID, core.CategorySourceLocal, 1.0); err != nil {
				return nil, err
			}
			res.Rules++
			res.Assigned = append(res.Assigned, Assignment{TxID: t.ID, CategoryID: catID, Category: candidates[catID].Name, Source: "local"})
			continue
		}

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

		if s.Provider != nil && len(candidates) > 0 {
			needAI = append(needAI, t)
			continue
		}

		if err := assign(tx, t, "", core.CategorySourceNone, 0); err != nil {
			return nil, err
		}
		res.Review++
	}

	if len(needAI) > 0 && s.Provider != nil {
		assigned, err := s.aiBatch(tx, needAI, aiThresh)
		if err != nil {
			for _, t := range needAI {
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

func (s *Service) aiBatch(tx *sql.Tx, txs []*core.Transaction, aiThresh float64) ([]Assignment, error) {
	type item struct {
		Index       int    `json:"index"`
		Date        string `json:"date"`
		Description string `json:"description"`
		AmountMinor int64  `json:"amount_minor"`
		Type        string `json:"type"`
	}
	catsByType := map[string]map[string]string{}
	descsByType := map[string]map[string]string{}
	for _, t := range txs {
		if _, ok := catsByType[t.Type]; ok {
			continue
		}
		cats, err := s.eligibleCategories(t.Type)
		if err != nil {
			return nil, err
		}
		m := map[string]string{}
		descs := map[string]string{}
		for id, c := range cats {
			m[id] = c.Name
			if c.Description != "" {
				descs[c.Name] = c.Description
			}
		}
		catsByType[t.Type] = m
		descsByType[t.Type] = descs
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
		prompt := map[string]any{
			"transactions":       items,
			"allowed_categories": catalogue,
		}
		if descs := descsByType[txType]; len(descs) > 0 {
			prompt["category_descriptions"] = descs
		}
		resp, _, err := s.Provider.CompleteStructured(&ai.StructuredRequest{
			Capability: "categorisation",
			PromptJSON: prompt,
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

func (s *Service) RefreshRules() error {
	return s.DB.WithTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM category_rules`); err != nil {
			return err
		}
		rows, err := tx.Query(`SELECT pattern, category_id, COUNT(*) AS votes FROM (
			SELECT ? AS pattern, category_id, 0 AS votes FROM transactions LIMIT 0)`, "")
		if err != nil {
			return err
		}
		rows.Close()
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

// RetrainQuiet rebuilds the model from the current data without a history
// entry; with no user-categorised rows left there is no model.
func (s *Service) RetrainQuiet() {
	model, err := s.TrainFromUserRows()
	if err != nil {
		s.Model = nil
		return
	}
	s.Model = model
}

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
