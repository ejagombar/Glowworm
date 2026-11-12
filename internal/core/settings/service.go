package settings

import (
	"database/sql"
	"strings"

	"glowworm/internal/core"
	"glowworm/internal/core/audit"
	"glowworm/internal/store"
	"glowworm/internal/store/repos"
)

const (
	KeyTheme          = "theme"
	KeyCurrency       = "currency"
	KeyUserName       = "user_name"
	KeyDefaultPeriod  = "default_period"
	KeyPrivacyDefault = "privacy_default"
	KeySetupCompleted = "setup_completed"
	KeyCatLocalThresh = "cat_local_threshold"
	KeyCatAIThresh    = "cat_ai_threshold"
	KeyCatMinExamples = "cat_min_examples"
	KeyTransferThresh = "transfer_threshold"
)

var notAudited = map[string]bool{
	KeyTheme: true, KeyPrivacyDefault: true, KeyDefaultPeriod: true,
	KeyUserName: true, KeyCurrency: true,
}

type Service struct {
	DB    *store.DB
	Repos *repos.Settings
	Audit *audit.Writer
}

type Snapshot struct {
	Theme          string
	Currency       string
	UserName       string
	DefaultPeriod  string
	PrivacyDefault bool
	SetupCompleted bool
}

func (s *Service) Get() *Snapshot {
	return &Snapshot{
		Theme:          s.Repos.GetString(KeyTheme),
		Currency:       s.Repos.GetString(KeyCurrency),
		UserName:       s.Repos.GetString(KeyUserName),
		DefaultPeriod:  s.Repos.GetString(KeyDefaultPeriod),
		PrivacyDefault: s.Repos.GetBool(KeyPrivacyDefault),
		SetupCompleted: s.Repos.GetBool(KeySetupCompleted),
	}
}

func (s *Service) All() (map[string]any, error) {
	return s.Repos.All()
}

func (s *Service) Patch(m map[string]any) error {
	allowed := map[string]bool{
		KeyTheme: true, KeyCurrency: true, KeyUserName: true, KeyDefaultPeriod: true,
		KeyPrivacyDefault: true, KeySetupCompleted: true,
		KeyCatLocalThresh: true, KeyCatAIThresh: true, KeyCatMinExamples: true,
		KeyTransferThresh: true,
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
				Sub:   "Changed: " + strings.Join(audited, ", "),
				After: audited,
			})
			if err != nil {
				return err
			}
		}
		return nil
	})
}
