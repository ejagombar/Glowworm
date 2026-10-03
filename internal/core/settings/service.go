// Package settings implements the settings service (doc 12): key catalogue,
// audit exemptions, and typed accessors.
package settings

import (
	"database/sql"

	"glowworm/internal/core"
	"glowworm/internal/core/audit"
	"glowworm/internal/store"
	"glowworm/internal/store/repos"
)

// Keys (doc 12 §1).
const (
	KeyTheme           = "theme"
	KeyCurrency        = "currency"
	KeyUserName        = "user_name"
	KeyDefaultPeriod   = "default_period"
	KeyPrivacyDefault  = "privacy_default"
	KeySetupCompleted  = "setup_completed"
	KeyCatLocalThresh  = "cat_local_threshold"
	KeyCatAIThresh     = "cat_ai_threshold"
	KeyCatMinExamples  = "cat_min_examples"
	KeyTransferThresh  = "transfer_threshold"
	KeyTransferWindow  = "transfer_date_window_days"
	KeyAIDailyTokenCap = "ai_daily_token_cap"
)

// notAudited per doc 12 §2.
var notAudited = map[string]bool{
	KeyTheme: true, KeyPrivacyDefault: true, KeyDefaultPeriod: true,
	KeyUserName: true, KeyCurrency: true,
}

// Service reads/writes settings.
type Service struct {
	DB    *store.DB
	Repos *repos.Settings
	Audit *audit.Writer
}

// Snapshot is the typed settings bundle used across handlers.
type Snapshot struct {
	Theme           string
	Currency        string
	UserName        string
	DefaultPeriod   string
	PrivacyDefault  bool
	SetupCompleted  bool
	CatLocalThresh  float64
	CatAIThresh     float64
	CatMinExamples  int
	TransferThresh  float64
	TransferWindow  int
	AIDailyTokenCap int
}

// Get loads the snapshot.
func (s *Service) Get() *Snapshot {
	return &Snapshot{
		Theme:           s.Repos.GetString(KeyTheme),
		Currency:        s.Repos.GetString(KeyCurrency),
		UserName:        s.Repos.GetString(KeyUserName),
		DefaultPeriod:   s.Repos.GetString(KeyDefaultPeriod),
		PrivacyDefault:  s.Repos.GetBool(KeyPrivacyDefault),
		SetupCompleted:  s.Repos.GetBool(KeySetupCompleted),
		CatLocalThresh:  s.Repos.GetFloat(KeyCatLocalThresh, 0.75),
		CatAIThresh:     s.Repos.GetFloat(KeyCatAIThresh, 0.8),
		CatMinExamples:  int(s.Repos.GetFloat(KeyCatMinExamples, 3)),
		TransferThresh:  s.Repos.GetFloat(KeyTransferThresh, 0.7),
		TransferWindow:  int(s.Repos.GetFloat(KeyTransferWindow, 4)),
		AIDailyTokenCap: int(s.Repos.GetFloat(KeyAIDailyTokenCap, 0)),
	}
}

// All returns the raw settings map (GET /api/v1/settings).
func (s *Service) All() (map[string]any, error) {
	return s.Repos.All()
}

// Patch applies allowed keys (audit only for data-affecting ones).
func (s *Service) Patch(m map[string]any) error {
	allowed := map[string]bool{
		KeyTheme: true, KeyCurrency: true, KeyUserName: true, KeyDefaultPeriod: true,
		KeyPrivacyDefault: true, KeySetupCompleted: true,
		KeyCatLocalThresh: true, KeyCatAIThresh: true, KeyCatMinExamples: true,
		KeyTransferThresh: true, KeyTransferWindow: true, KeyAIDailyTokenCap: true,
	}
	return s.DB.WithTx(func(tx *sql.Tx) error {
		var audited []string
		for k, v := range m {
			if !allowed[k] {
				continue
			}
			if err := s.Repos.Set(tx, k, v); err != nil {
				return err
			}
			if !notAudited[k] {
				audited = append(audited, k)
			}
		}
		if len(audited) > 0 {
			_, err := s.Audit.Write(tx, audit.Input{
				Actor: core.ActorUser, Action: "settings.updated",
				EntityType: "settings", EntityID: "app",
				Title: "Updated settings",
				Sub:   "Changed: " + joinKeys(audited),
				After: audited,
			})
			if err != nil {
				return err
			}
		}
		return nil
	})
}

func joinKeys(keys []string) string {
	out := ""
	for i, k := range keys {
		if i > 0 {
			out += ", "
		}
		out += k
	}
	return out
}