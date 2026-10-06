package repos

import (
	"database/sql"
	"strings"

	"glowworm/internal/core"
	"glowworm/internal/store"
	"glowworm/internal/util"
)

type Accounts struct{ DB *store.DB }

const accountCols = `id, name, type, color, currency, opening_balance_minor, COALESCE(opening_balance_date,''),
	account_number, sort_code, iban, bic, notes, created_at, updated_at, deleted_at`

func scanAccount(sc Scanner) (*core.Account, error) {
	var a core.Account
	var openingDate, deletedAt sql.NullString
	err := sc.Scan(&a.ID, &a.Name, &a.Type, &a.Color, &a.Currency, &a.OpeningBalanceMinor, &openingDate,
		&a.AccountNumber, &a.SortCode, &a.IBAN, &a.BIC, &a.Notes, &a.CreatedAt, &a.UpdatedAt, &deletedAt)
	if err != nil {
		return nil, err
	}
	a.OpeningBalanceDate = openingDate.String
	if deletedAt.Valid {
		s := deletedAt.String
		a.DeletedAt = &s
	}
	a.HasNumber = a.AccountNumber != ""
	a.HasIBAN = a.IBAN != ""
	a.HasBIC = a.BIC != ""
	a.RefLast4 = Last4(a.AccountNumber)
	return &a, nil
}

func Last4(s string) string {
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
	if len(digits) < 4 {
		return ""
	}
	return digits[len(digits)-4:]
}

func (r *Accounts) List() ([]*core.Account, error) {
	rows, err := r.DB.Query(`SELECT ` + accountCols + ` FROM accounts WHERE deleted_at IS NULL ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*core.Account
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (r *Accounts) Get(id string) (*core.Account, error) {
	return scanAccount(r.DB.QueryRow(`SELECT `+accountCols+` FROM accounts WHERE id = ? AND deleted_at IS NULL`, id))
}

func (r *Accounts) NextColor() string {
	var n int
	_ = r.DB.QueryRow(`SELECT COUNT(*) FROM accounts WHERE deleted_at IS NULL`).Scan(&n)
	return core.Palette[n%len(core.Palette)]
}

func (r *Accounts) Create(tx *sql.Tx, a *core.Account) error {
	a.ID = util.NewID()
	a.CreatedAt = util.NowStr()
	a.UpdatedAt = a.CreatedAt
	if a.Color == "" {
		a.Color = r.NextColor()
	}
	_, err := exec(r.DB, tx, `INSERT INTO accounts
		(id, name, type, color, currency, opening_balance_minor, opening_balance_date, account_number, sort_code, iban, bic, notes, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.ID, a.Name, a.Type, a.Color, a.Currency, a.OpeningBalanceMinor, nullIfEmpty(a.OpeningBalanceDate),
		a.AccountNumber, a.SortCode, a.IBAN, a.BIC, a.Notes, a.CreatedAt, a.UpdatedAt)
	return err
}

func (r *Accounts) Update(tx *sql.Tx, a *core.Account) error {
	a.UpdatedAt = util.NowStr()
	_, err := exec(r.DB, tx, `UPDATE accounts SET name=?, type=?, color=?, currency=?, opening_balance_minor=?,
		opening_balance_date=?, account_number=?, sort_code=?, iban=?, bic=?, notes=?, updated_at=? WHERE id=? AND deleted_at IS NULL`,
		a.Name, a.Type, a.Color, a.Currency, a.OpeningBalanceMinor, nullIfEmpty(a.OpeningBalanceDate),
		a.AccountNumber, a.SortCode, a.IBAN, a.BIC, a.Notes, a.UpdatedAt, a.ID)
	return err
}

func (r *Accounts) SoftDelete(tx *sql.Tx, id string) error {
	_, err := exec(r.DB, tx, `UPDATE accounts SET deleted_at = ? WHERE id = ?`, util.NowStr(), id)
	return err
}

func (r *Accounts) Restore(tx *sql.Tx, id string) error {
	_, err := exec(r.DB, tx, `UPDATE accounts SET deleted_at = NULL WHERE id = ?`, id)
	return err
}

func (r *Accounts) Delete(id string) error {
	_, err := r.DB.Exec(`DELETE FROM accounts WHERE id = ?`, id)
	return err
}

func (r *Accounts) CreateValuation(tx *sql.Tx, v *core.Valuation) error {
	v.ID = util.NewID()
	v.CreatedAt = util.NowStr()
	v.UpdatedAt = v.CreatedAt
	var auditID, drift any
	if v.AuditLogID != nil {
		auditID = *v.AuditLogID
	}
	if v.DriftMinor != nil {
		drift = *v.DriftMinor
	}
	_, err := exec(r.DB, tx, `INSERT INTO valuations (id, account_id, value_minor, date, note, source, drift_minor, audit_log_id, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`,
		v.ID, v.AccountID, v.ValueMinor, v.Date, v.Note, v.Source, drift, auditID, v.CreatedAt, v.UpdatedAt)
	return err
}

func (r *Accounts) Valuations(accountID string) ([]*core.Valuation, error) {
	rows, err := r.DB.Query(`SELECT id, account_id, value_minor, date, note, source, drift_minor, audit_log_id, created_at, updated_at
		FROM valuations WHERE account_id = ? AND deleted_at IS NULL ORDER BY date ASC, created_at ASC`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*core.Valuation
	for rows.Next() {
		v, err := scanValuation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func scanValuation(sc Scanner) (*core.Valuation, error) {
	var v core.Valuation
	var drift, auditID sql.NullInt64
	var deletedAt sql.NullString
	err := sc.Scan(&v.ID, &v.AccountID, &v.ValueMinor, &v.Date, &v.Note, &v.Source, &drift, &auditID, &v.CreatedAt, &v.UpdatedAt, &deletedAt)
	if err != nil {
		return nil, err
	}
	if drift.Valid {
		d := drift.Int64
		v.DriftMinor = &d
	}
	if auditID.Valid {
		a := auditID.Int64
		v.AuditLogID = &a
	}
	if deletedAt.Valid {
		s := deletedAt.String
		v.DeletedAt = &s
	}
	return &v, nil
}

func (r *Accounts) GetValuation(id string) (*core.Valuation, error) {
	return scanValuation(r.DB.QueryRow(`SELECT id, account_id, value_minor, date, note, source, drift_minor, audit_log_id, created_at, updated_at, deleted_at
		FROM valuations WHERE id = ?`, id))
}

func (r *Accounts) DeleteValuation(tx *sql.Tx, id string) error {
	_, err := exec(r.DB, tx, `UPDATE valuations SET deleted_at = ? WHERE id = ?`, util.NowStr(), id)
	return err
}

func (r *Accounts) RestoreValuation(tx *sql.Tx, id string) error {
	_, err := exec(r.DB, tx, `UPDATE valuations SET deleted_at = NULL WHERE id = ?`, id)
	return err
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
