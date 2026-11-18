package categoriser

import (
	"database/sql"
	"fmt"

	"github.com/ejagombar/Glowworm/internal/ai"
	"github.com/ejagombar/Glowworm/internal/classifier"
	"github.com/ejagombar/Glowworm/internal/core"
	"github.com/ejagombar/Glowworm/internal/core/audit"
	"github.com/ejagombar/Glowworm/internal/core/normalize"
	"github.com/ejagombar/Glowworm/internal/store"
	"github.com/ejagombar/Glowworm/internal/store/repos"
	"github.com/ejagombar/Glowworm/internal/util"
)

type Service struct {
	DB         *store.DB
	Categories *repos.Categories
	Settings   *repos.Settings
	Audit      *audit.Writer
	Provider   ai.Provider
	Model      *classifier.Model
}

func (s *Service) RefreshRules() error {
	return s.DB.WithTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM category_rules`); err != nil {
			return err
		}
		agg := map[string]map[string]int{}
		rows, err := tx.Query(`SELECT description, category_id FROM transactions
			WHERE deleted_at IS NULL AND category_source = 'user' AND category_id IS NOT NULL`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var desc, catID string
			if err := rows.Scan(&desc, &catID); err != nil {
				return err
			}
			pattern := normalize.Text(desc)
			if agg[pattern] == nil {
				agg[pattern] = map[string]int{}
			}
			agg[pattern][catID]++
		}
		rows.Close()
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
					VALUES (?,?,?,?)`, pattern, best, bestN, util.NowStr()); err != nil {
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
	model, err := classifier.Train(samples, version, util.NowStr())
	if err != nil {
		return nil, err
	}
	payload, err := model.Encode()
	if err != nil {
		return nil, err
	}
	_, err = s.DB.Exec(`INSERT INTO classifier_models (version, trained_at, training_count, payload, metrics)
		VALUES (?,?,?,?,?)`, version, util.NowStr(), len(samples), payload, "{}")
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
