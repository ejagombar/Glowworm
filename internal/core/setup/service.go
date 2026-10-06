package setup

import (
	"database/sql"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"

	"glowworm/internal/core"
	"glowworm/internal/core/audit"
	"glowworm/internal/store"
	"glowworm/internal/store/repos"
)

type Service struct {
	DB       *store.DB
	Users    *repos.Users
	Settings *repos.Settings
	Audit    *audit.Writer
}

type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

type Status struct {
	Completed       bool `json:"completed"`
	HasCategories   bool `json:"has_categories"`
	HasAccounts     bool `json:"has_accounts"`
	HasTransactions bool `json:"has_transactions"`
	AIConfigured    bool `json:"ai_configured"`
	HasUser         bool `json:"has_user"`
}

func (s *Service) Status() (*Status, error) {
	st := &Status{}
	st.Completed = s.Settings.GetBool("setup_completed")
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM categories WHERE deleted_at IS NULL AND is_system = 0`).Scan(&n); err != nil {
		return nil, err
	}
	st.HasCategories = n > 0
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM accounts WHERE deleted_at IS NULL`).Scan(&n); err != nil {
		return nil, err
	}
	st.HasAccounts = n > 0
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM transactions WHERE deleted_at IS NULL`).Scan(&n); err != nil {
		return nil, err
	}
	st.HasTransactions = n > 0
	var provider string
	_ = s.DB.QueryRow(`SELECT provider FROM ai_provider_config WHERE id = 1`).Scan(&provider)
	st.AIConfigured = provider != ""
	users, err := s.Users.Count()
	if err != nil {
		return nil, err
	}
	st.HasUser = users > 0
	return st, nil
}

type AccountInput struct {
	Username        string `json:"username"`
	Password        string `json:"password"`
	PasswordConfirm string `json:"password_confirm"`
	DisplayName     string `json:"display_name"`
}

func (in *AccountInput) Validate() error {
	in.Username = strings.TrimSpace(in.Username)
	if utf8.RuneCountInString(in.Username) < 3 || utf8.RuneCountInString(in.Username) > 40 {
		return &ValidationError{Message: "Username must be 3–40 characters"}
	}
	if utf8.RuneCountInString(in.Password) < 10 {
		return &ValidationError{Message: "Password must be at least 10 characters"}
	}
	if in.Password != in.PasswordConfirm {
		return &ValidationError{Message: "Passwords do not match"}
	}
	return nil
}

func (s *Service) CreateAccount(in AccountInput) (*core.User, error) {
	if users, _ := s.Users.Count(); users > 0 {
		return nil, &ValidationError{Message: "An owner account already exists for this instance"}
	}
	if err := in.Validate(); err != nil {
		return nil, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), 12)
	if err != nil {
		return nil, err
	}
	u := &core.User{Username: in.Username, DisplayName: strings.TrimSpace(in.DisplayName)}
	if u.DisplayName == "" {
		u.DisplayName = u.Username
	}
	err = s.DB.WithTx(func(tx *sql.Tx) error {
		if err := s.Users.Create(tx, u, string(hash)); err != nil {
			return err
		}
		_, err := s.Audit.Write(tx, audit.Input{
			Actor: core.ActorUser, Action: "setup.account_created",
			EntityType: "user", EntityID: u.ID,
			Title: "Owner account created", Sub: u.Username,
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	return u, nil
}

func (s *Service) Complete() error {
	return s.DB.WithTx(func(tx *sql.Tx) error {
		if err := s.Settings.Set(tx, "setup_completed", true); err != nil {
			return err
		}
		_, err := s.Audit.Write(tx, audit.Input{
			Actor: core.ActorUser, Action: "setup.completed",
			EntityType: "settings", EntityID: "setup_completed",
			Title: "Setup completed",
		})
		return err
	})
}
