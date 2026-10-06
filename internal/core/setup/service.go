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
	if err := in.Validate(); err != nil {
		return nil, err
	}
	return s.CreateOwner(in.Username, in.Password, in.DisplayName, core.ActorUser, "")
}

// CreateOwner makes the instance's single account. Terminal and config
// bootstraps pass the system actor and say where the account came from.
func (s *Service) CreateOwner(username, password, displayName, actor, via string) (*core.User, error) {
	if users, _ := s.Users.Count(); users > 0 {
		return nil, &ValidationError{Message: "An owner account already exists for this instance"}
	}
	username = strings.TrimSpace(username)
	if n := utf8.RuneCountInString(username); n < 3 || n > 40 {
		return nil, &ValidationError{Message: "Username must be 3–40 characters"}
	}
	if utf8.RuneCountInString(password) < 10 {
		return nil, &ValidationError{Message: "Password must be at least 10 characters"}
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return nil, err
	}
	u := &core.User{Username: username, DisplayName: strings.TrimSpace(displayName)}
	if u.DisplayName == "" {
		u.DisplayName = u.Username
	}
	sub := u.Username
	if via != "" {
		sub += " · " + via
	}
	err = s.DB.WithTx(func(tx *sql.Tx) error {
		if err := s.Users.Create(tx, u, string(hash)); err != nil {
			return err
		}
		_, err := s.Audit.Write(tx, audit.Input{
			Actor: actor, Action: "setup.account_created",
			EntityType: "user", EntityID: u.ID,
			Title: "Owner account created", Sub: sub,
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	return u, nil
}
