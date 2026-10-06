package accounts

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"glowworm/internal/core"
	"glowworm/internal/core/audit"
	"glowworm/internal/store/repos"
	"glowworm/internal/util"
)

const (
	BackupFormat  = "glowworm.account-backup"
	BackupVersion = 1
)

type Backup struct {
	Format     string           `json:"format"`
	Version    int              `json:"version"`
	CreatedAt  string           `json:"created_at"`
	Accounts   []AccountBackup  `json:"accounts"`
	Categories []BackupCategory `json:"categories"`
}

type AccountBackup struct {
	Account      BackupAccount       `json:"account"`
	Valuations   []BackupValuation   `json:"valuations"`
	Transactions []BackupTransaction `json:"transactions"`
	Transfers    []BackupTransfer    `json:"transfers"`
	SchemaIDs    []string            `json:"schema_ids,omitempty"`
}

type BackupAccount struct {
	ID                  string `json:"id"`
	Name                string `json:"name"`
	Type                string `json:"type"`
	Color               string `json:"color"`
	Currency            string `json:"currency"`
	OpeningBalanceMinor int64  `json:"opening_balance_minor"`
	OpeningBalanceDate  string `json:"opening_balance_date,omitempty"`
	AccountNumber       string `json:"account_number,omitempty"`
	SortCode            string `json:"sort_code,omitempty"`
	IBAN                string `json:"iban,omitempty"`
	BIC                 string `json:"bic,omitempty"`
	Notes               string `json:"notes,omitempty"`
	CreatedAt           string `json:"created_at"`
	UpdatedAt           string `json:"updated_at"`
}

type BackupValuation struct {
	ID         string `json:"id"`
	ValueMinor int64  `json:"value_minor"`
	Date       string `json:"date"`
	Note       string `json:"note,omitempty"`
	Source     string `json:"source"`
	DriftMinor *int64 `json:"drift_minor,omitempty"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

type BackupTransaction struct {
	ID                 string   `json:"id"`
	Date               string   `json:"date"`
	Description        string   `json:"description"`
	Merchant           *string  `json:"merchant,omitempty"`
	AmountMinor        int64    `json:"amount_minor"`
	Currency           string   `json:"currency"`
	Type               string   `json:"type"`
	CategoryID         *string  `json:"category_id,omitempty"`
	CategorySource     string   `json:"category_source"`
	CategoryConfidence *float64 `json:"category_confidence,omitempty"`
	SourceText         string   `json:"source_text,omitempty"`
	DestinationText    string   `json:"destination_text,omitempty"`
	RecurringKey       *string  `json:"recurring_key,omitempty"`
	TransferID         *string  `json:"transfer_id,omitempty"`
	ImportBatchID      *string  `json:"import_batch_id,omitempty"`
	DedupeHash         string   `json:"dedupe_hash"`
	CreatedAt          string   `json:"created_at"`
	UpdatedAt          string   `json:"updated_at"`
}

type BackupTransfer struct {
	ID                       string   `json:"id"`
	SourceTransactionID      string   `json:"source_transaction_id"`
	DestinationTransactionID string   `json:"destination_transaction_id"`
	Detected                 string   `json:"detected"`
	Method                   string   `json:"method,omitempty"`
	Confidence               *float64 `json:"confidence,omitempty"`
	CreatedAt                string   `json:"created_at"`
}

type BackupCategory struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color"`
	Type  string `json:"type"`
}

type Archive struct {
	ID        string `json:"id"`
	AccountID string `json:"account_id"`
	Name      string `json:"name"`
	TxCount   int    `json:"tx_count"`
	DeletedAt string `json:"deleted_at"`
}

type RestoredAccount struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Transactions int    `json:"transactions"`
	Valuations   int    `json:"valuations"`
	Transfers    int    `json:"transfers"`
}

type RestoreResult struct {
	Accounts          []RestoredAccount `json:"accounts"`
	Skipped           []string          `json:"skipped,omitempty"`
	CategoriesCreated []string          `json:"categories_created,omitempty"`
}

type querier interface {
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

func (s *Service) Backup(ids []string) (*Backup, error) {
	if len(ids) == 0 {
		list, err := s.Repos.List()
		if err != nil {
			return nil, err
		}
		for _, a := range list {
			ids = append(ids, a.ID)
		}
	}
	return buildBackup(s.DB, ids)
}

func buildBackup(q querier, ids []string) (*Backup, error) {
	b := &Backup{Format: BackupFormat, Version: BackupVersion, CreatedAt: util.NowStr(),
		Accounts: []AccountBackup{}, Categories: []BackupCategory{}}
	catIDs := map[string]bool{}
	for _, id := range ids {
		ab, err := loadAccountBackup(q, id)
		if err != nil {
			return nil, err
		}
		for _, t := range ab.Transactions {
			if t.CategoryID != nil {
				catIDs[*t.CategoryID] = true
			}
		}
		b.Accounts = append(b.Accounts, *ab)
	}
	for id := range catIDs {
		var c BackupCategory
		err := q.QueryRow(`SELECT id, name, color, type FROM categories WHERE id = ?`, id).Scan(&c.ID, &c.Name, &c.Color, &c.Type)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return nil, err
		}
		b.Categories = append(b.Categories, c)
	}
	return b, nil
}

func loadAccountBackup(q querier, id string) (*AccountBackup, error) {
	ab := &AccountBackup{Valuations: []BackupValuation{}, Transactions: []BackupTransaction{}, Transfers: []BackupTransfer{}}
	a := &ab.Account
	var openDate sql.NullString
	err := q.QueryRow(`SELECT id, name, type, color, currency, opening_balance_minor, opening_balance_date,
		account_number, sort_code, iban, bic, notes, created_at, updated_at
		FROM accounts WHERE id = ? AND deleted_at IS NULL`, id).
		Scan(&a.ID, &a.Name, &a.Type, &a.Color, &a.Currency, &a.OpeningBalanceMinor, &openDate,
			&a.AccountNumber, &a.SortCode, &a.IBAN, &a.BIC, &a.Notes, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return nil, err
	}
	a.OpeningBalanceDate = openDate.String

	rows, err := q.Query(`SELECT id, value_minor, date, note, source, drift_minor, created_at, updated_at
		FROM valuations WHERE account_id = ? AND deleted_at IS NULL ORDER BY date, created_at`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var v BackupValuation
		var drift sql.NullInt64
		if err := rows.Scan(&v.ID, &v.ValueMinor, &v.Date, &v.Note, &v.Source, &drift, &v.CreatedAt, &v.UpdatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		if drift.Valid {
			v.DriftMinor = &drift.Int64
		}
		ab.Valuations = append(ab.Valuations, v)
	}
	rows.Close()

	rows, err = q.Query(`SELECT id, date, description, merchant, amount_minor, currency, type, category_id,
		category_source, category_confidence, source_text, destination_text, recurring_key, transfer_id,
		import_batch_id, dedupe_hash, created_at, updated_at
		FROM transactions WHERE account_id = ? AND deleted_at IS NULL ORDER BY date, created_at`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var t BackupTransaction
		var merchant, catID, recKey, transferID, batchID sql.NullString
		var conf sql.NullFloat64
		if err := rows.Scan(&t.ID, &t.Date, &t.Description, &merchant, &t.AmountMinor, &t.Currency, &t.Type, &catID,
			&t.CategorySource, &conf, &t.SourceText, &t.DestinationText, &recKey, &transferID,
			&batchID, &t.DedupeHash, &t.CreatedAt, &t.UpdatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		t.Merchant, t.CategoryID, t.RecurringKey = optStr(merchant), optStr(catID), optStr(recKey)
		t.TransferID, t.ImportBatchID = optStr(transferID), optStr(batchID)
		if conf.Valid {
			t.CategoryConfidence = &conf.Float64
		}
		ab.Transactions = append(ab.Transactions, t)
	}
	rows.Close()

	rows, err = q.Query(`SELECT id, source_transaction_id, destination_transaction_id, detected, method, confidence, created_at
		FROM transfers WHERE deleted_at IS NULL AND (
			source_transaction_id IN (SELECT id FROM transactions WHERE account_id = ? AND deleted_at IS NULL) OR
			destination_transaction_id IN (SELECT id FROM transactions WHERE account_id = ? AND deleted_at IS NULL))`, id, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var tr BackupTransfer
		var conf sql.NullFloat64
		if err := rows.Scan(&tr.ID, &tr.SourceTransactionID, &tr.DestinationTransactionID, &tr.Detected, &tr.Method, &conf, &tr.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		if conf.Valid {
			tr.Confidence = &conf.Float64
		}
		ab.Transfers = append(ab.Transfers, tr)
	}
	rows.Close()

	rows, err = q.Query(`SELECT id FROM import_schemas WHERE account_id = ?`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var sid string
		if err := rows.Scan(&sid); err != nil {
			rows.Close()
			return nil, err
		}
		ab.SchemaIDs = append(ab.SchemaIDs, sid)
	}
	rows.Close()
	return ab, nil
}

func (s *Service) Delete(id string) (*Archive, error) {
	var arc *Archive
	err := s.DB.WithTx(func(tx *sql.Tx) error {
		b, err := buildBackup(tx, []string{id})
		if err != nil {
			return err
		}
		ab := b.Accounts[0]
		payload, err := json.Marshal(b)
		if err != nil {
			return err
		}
		arc = &Archive{ID: util.NewID(), AccountID: id, Name: ab.Account.Name,
			TxCount: len(ab.Transactions), DeletedAt: util.NowStr()}
		if _, err := tx.Exec(`INSERT INTO account_archives (id, account_id, name, tx_count, deleted_at, payload)
			VALUES (?,?,?,?,?,?)`, arc.ID, arc.AccountID, arc.Name, arc.TxCount, arc.DeletedAt, string(payload)); err != nil {
			return err
		}
		if err := purgeAccount(tx, id); err != nil {
			return err
		}
		_, err = s.Audit.Write(tx, audit.Input{
			Actor: core.ActorUser, Action: "account.deleted",
			EntityType: "account", EntityID: id,
			Title: "Deleted account",
			Sub:   fmt.Sprintf("%s · %s · restorable from Settings", ab.Account.Name, plural(arc.TxCount, "transaction")),
			Before: map[string]any{
				"name": ab.Account.Name, "type": ab.Account.Type, "currency": ab.Account.Currency,
				"transactions": arc.TxCount, "valuations": len(ab.Valuations),
			},
			After: map[string]any{"archive_id": arc.ID},
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	s.afterChange()
	return arc, nil
}

// Foreign keys aren't enforced on every pooled connection (see store.Open), so
// dependents are deleted explicitly instead of relying on ON DELETE CASCADE.
func purgeAccount(tx *sql.Tx, id string) error {
	internalCat, err := internalCategoryID(tx)
	if err != nil {
		return err
	}
	const legs = `SELECT id FROM transactions WHERE account_id = ?`
	if _, err := tx.Exec(`UPDATE transactions SET
			type = CASE WHEN amount_minor > 0 THEN 'income' ELSE 'expense' END,
			transfer_id = NULL,
			category_id = CASE WHEN category_id = ? THEN NULL ELSE category_id END,
			category_source = CASE WHEN category_id = ? THEN 'none' ELSE category_source END,
			category_confidence = CASE WHEN category_id = ? THEN NULL ELSE category_confidence END,
			updated_at = ?
		WHERE account_id != ? AND id IN (
			SELECT source_transaction_id FROM transfers WHERE destination_transaction_id IN (`+legs+`)
			UNION SELECT destination_transaction_id FROM transfers WHERE source_transaction_id IN (`+legs+`))`,
		internalCat, internalCat, internalCat, util.NowStr(), id, id, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM transfers WHERE source_transaction_id IN (`+legs+`)
		OR destination_transaction_id IN (`+legs+`)`, id, id); err != nil {
		return err
	}
	for _, q := range []string{
		`UPDATE import_staging SET duplicate_of = NULL WHERE duplicate_of IN (` + legs + `)`,
		`DELETE FROM transactions WHERE account_id = ?`,
		`DELETE FROM valuations WHERE account_id = ?`,
		`UPDATE import_batches SET account_id = NULL WHERE account_id = ?`,
		`UPDATE import_schemas SET account_id = NULL WHERE account_id = ?`,
		`DELETE FROM accounts WHERE id = ?`,
	} {
		if _, err := tx.Exec(q, id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) Archives() ([]*Archive, error) {
	rows, err := s.DB.Query(`SELECT id, account_id, name, tx_count, deleted_at FROM account_archives ORDER BY deleted_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Archive{}
	for rows.Next() {
		var a Archive
		if err := rows.Scan(&a.ID, &a.AccountID, &a.Name, &a.TxCount, &a.DeletedAt); err != nil {
			return nil, err
		}
		out = append(out, &a)
	}
	return out, rows.Err()
}

func (s *Service) ArchiveBackup(archiveID string) (*Archive, *Backup, error) {
	var a Archive
	var payload string
	err := s.DB.QueryRow(`SELECT id, account_id, name, tx_count, deleted_at, payload FROM account_archives WHERE id = ?`, archiveID).
		Scan(&a.ID, &a.AccountID, &a.Name, &a.TxCount, &a.DeletedAt, &payload)
	if err != nil {
		return nil, nil, err
	}
	var b Backup
	if err := json.Unmarshal([]byte(payload), &b); err != nil {
		return nil, nil, fmt.Errorf("archive %s is unreadable: %w", archiveID, err)
	}
	return &a, &b, nil
}

func (s *Service) PurgeArchive(archiveID string) error {
	arc, _, err := s.ArchiveBackup(archiveID)
	if err != nil {
		return err
	}
	return s.DB.WithTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM account_archives WHERE id = ?`, archiveID); err != nil {
			return err
		}
		_, err := s.Audit.Write(tx, audit.Input{
			Actor: core.ActorUser, Action: "account.purged",
			EntityType: "account", EntityID: arc.AccountID,
			Title: "Permanently deleted account",
			Sub:   fmt.Sprintf("%s · %s · no longer restorable", arc.Name, plural(arc.TxCount, "transaction")),
		})
		return err
	})
}

func (s *Service) RestoreArchive(archiveID string) (*RestoreResult, error) {
	_, b, err := s.ArchiveBackup(archiveID)
	if err != nil {
		return nil, err
	}
	return s.restore(b, archiveID)
}

func (s *Service) RestoreBackup(b *Backup) (*RestoreResult, error) {
	return s.restore(b, "")
}

func ValidateBackup(b *Backup) error {
	if b.Format != BackupFormat {
		return &ValidationError{Message: "That isn't a Glowworm account backup file"}
	}
	if b.Version < 1 || b.Version > BackupVersion {
		return &ValidationError{Message: fmt.Sprintf("This backup is format version %d; this Glowworm reads up to version %d", b.Version, BackupVersion)}
	}
	if len(b.Accounts) == 0 {
		return &ValidationError{Message: "The backup contains no accounts"}
	}
	seenAcc, seenTx := map[string]bool{}, map[string]bool{}
	for _, ab := range b.Accounts {
		a := ab.Account
		if a.ID == "" || strings.TrimSpace(a.Name) == "" || !validType(a.Type) || a.Currency == "" {
			return &ValidationError{Message: fmt.Sprintf("Account %q in the backup is incomplete", a.Name)}
		}
		if seenAcc[a.ID] {
			return &ValidationError{Message: fmt.Sprintf("Account %q appears twice in the backup", a.Name)}
		}
		seenAcc[a.ID] = true
		if a.OpeningBalanceDate != "" && !isDate(a.OpeningBalanceDate) {
			return &ValidationError{Message: fmt.Sprintf("Account %q has a bad opening date", a.Name)}
		}
		for _, v := range ab.Valuations {
			if v.ID == "" || !isDate(v.Date) {
				return &ValidationError{Message: fmt.Sprintf("A valuation in %q is malformed", a.Name)}
			}
		}
		for _, t := range ab.Transactions {
			if t.ID == "" || !isDate(t.Date) || t.Currency == "" ||
				(t.Type != core.TypeExpense && t.Type != core.TypeIncome && t.Type != core.TypeTransfer) {
				return &ValidationError{Message: fmt.Sprintf("Transaction %q in %q is malformed", t.Description, a.Name)}
			}
			switch t.CategorySource {
			case "", core.CategorySourceUser, core.CategorySourceLocal, core.CategorySourceAI, core.CategorySourceNone:
			default:
				return &ValidationError{Message: fmt.Sprintf("Transaction %q in %q has an unknown category source", t.Description, a.Name)}
			}
			if seenTx[t.ID] {
				return &ValidationError{Message: "The backup lists the same transaction twice"}
			}
			seenTx[t.ID] = true
		}
	}
	for _, c := range b.Categories {
		if c.ID == "" || strings.TrimSpace(c.Name) == "" ||
			(c.Type != core.CategorySpend && c.Type != core.CategoryIncome && c.Type != core.CategoryInternal) {
			return &ValidationError{Message: fmt.Sprintf("Category %q in the backup is malformed", c.Name)}
		}
	}
	return nil
}

func (s *Service) restore(b *Backup, archiveID string) (*RestoreResult, error) {
	if err := ValidateBackup(b); err != nil {
		return nil, err
	}
	res := &RestoreResult{}
	source := "a backup file"
	if archiveID != "" {
		source = "Deleted accounts"
	}
	err := s.DB.WithTx(func(tx *sql.Tx) error {
		var restore []AccountBackup
		var clashes []string
		usedCats := map[string]bool{}
		for _, ab := range b.Accounts {
			var name string
			err := tx.QueryRow(`SELECT name FROM accounts WHERE id = ?`, ab.Account.ID).Scan(&name)
			switch {
			case err == nil:
				clashes = append(clashes, name)
			case err == sql.ErrNoRows:
				restore = append(restore, ab)
				for _, t := range ab.Transactions {
					if t.CategoryID != nil {
						usedCats[*t.CategoryID] = true
					}
				}
			default:
				return err
			}
		}
		res.Skipped = clashes
		if len(restore) == 0 {
			return &ValidationError{Message: fmt.Sprintf("%s already exists — delete it first to replace it with this backup",
				quoteJoin(clashes))}
		}
		var cats []BackupCategory
		for _, c := range b.Categories {
			if usedCats[c.ID] {
				cats = append(cats, c)
			}
		}
		catMap, created, err := mapCategories(tx, cats)
		if err != nil {
			return err
		}
		res.CategoriesCreated = created
		internalCat, err := internalCategoryID(tx)
		if err != nil {
			return err
		}
		now := util.NowStr()

		txIDs := map[string]string{}
		for i := range restore {
			ab := &restore[i]
			a := ab.Account
			if _, err := tx.Exec(`INSERT INTO accounts (id, name, type, color, currency, opening_balance_minor,
				opening_balance_date, account_number, sort_code, iban, bic, notes, created_at, updated_at)
				VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				a.ID, a.Name, a.Type, orDefault(a.Color, "#0e6f51"), a.Currency, a.OpeningBalanceMinor, nullIfEmpty(a.OpeningBalanceDate),
				a.AccountNumber, a.SortCode, a.IBAN, a.BIC, a.Notes, orDefault(a.CreatedAt, now), now); err != nil {
				return err
			}
			for _, v := range ab.Valuations {
				vid := freeID(tx, "valuations", v.ID)
				var drift any
				if v.DriftMinor != nil {
					drift = *v.DriftMinor
				}
				if _, err := tx.Exec(`INSERT INTO valuations (id, account_id, value_minor, date, note, source, drift_minor, created_at, updated_at)
					VALUES (?,?,?,?,?,?,?,?,?)`, vid, a.ID, v.ValueMinor, v.Date, v.Note, orDefault(v.Source, "manual"),
					drift, orDefault(v.CreatedAt, now), now); err != nil {
					return err
				}
			}
			for _, t := range ab.Transactions {
				id := freeID(tx, "transactions", t.ID)
				txIDs[t.ID] = id
				var catID any
				if t.CategoryID != nil {
					if mapped, ok := catMap[*t.CategoryID]; ok {
						catID = mapped
					} else if categoryExists(tx, *t.CategoryID) {
						catID = *t.CategoryID
					}
				}
				var conf any
				if t.CategoryConfidence != nil {
					conf = *t.CategoryConfidence
				}
				hash := t.DedupeHash
				if hash == "" {
					hash = repos.DedupeHash(a.ID, t.Date, t.AmountMinor, t.Description)
				}
				if _, err := tx.Exec(`INSERT INTO transactions (id, account_id, date, description, merchant, amount_minor,
					currency, type, category_id, category_source, category_confidence, source_text, destination_text,
					recurring_key, transfer_id, import_batch_id, dedupe_hash, created_at, updated_at)
					VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,NULL,?,?,?,?)`,
					id, a.ID, t.Date, t.Description, ptrVal(t.Merchant), t.AmountMinor, t.Currency, t.Type,
					catID, orDefault(t.CategorySource, core.CategorySourceNone), conf, t.SourceText, t.DestinationText,
					ptrVal(t.RecurringKey), ptrVal(t.ImportBatchID), hash, orDefault(t.CreatedAt, now), now); err != nil {
					return err
				}
			}
		}

		// A link between two accounts in the same backup is listed under both.
		relinked := map[string]int{}
		done := map[string]bool{}
		for _, ab := range restore {
			for _, tr := range ab.Transfers {
				if done[tr.ID] {
					continue
				}
				done[tr.ID] = true
				src, dst := mapped(txIDs, tr.SourceTransactionID), mapped(txIDs, tr.DestinationTransactionID)
				if !legFree(tx, src) || !legFree(tx, dst) {
					continue
				}
				trID := freeID(tx, "transfers", tr.ID)
				var conf any
				if tr.Confidence != nil {
					conf = *tr.Confidence
				}
				if _, err := tx.Exec(`INSERT INTO transfers (id, source_transaction_id, destination_transaction_id, detected, method, confidence, created_at)
					VALUES (?,?,?,?,?,?,?)`, trID, src, dst, orDefault(tr.Detected, "manual"), tr.Method, conf, orDefault(tr.CreatedAt, now)); err != nil {
					return err
				}
				for _, leg := range []string{src, dst} {
					if _, err := tx.Exec(`UPDATE transactions SET type = 'transfer', transfer_id = ?, category_id = ?,
						category_source = 'none', category_confidence = NULL, updated_at = ? WHERE id = ?`,
						trID, nullIfEmpty(internalCat), now, leg); err != nil {
						return err
					}
					var acct string
					_ = tx.QueryRow(`SELECT account_id FROM transactions WHERE id = ?`, leg).Scan(&acct)
					relinked[acct]++
				}
			}
		}

		for _, ab := range restore {
			a := ab.Account
			if _, err := tx.Exec(`UPDATE transactions SET
					type = CASE WHEN amount_minor > 0 THEN 'income' ELSE 'expense' END,
					category_id = CASE WHEN category_id = ? THEN NULL ELSE category_id END,
					category_source = CASE WHEN category_id = ? THEN 'none' ELSE category_source END
				WHERE account_id = ? AND type = 'transfer' AND transfer_id IS NULL`, internalCat, internalCat, a.ID); err != nil {
				return err
			}
			if _, err := tx.Exec(`UPDATE import_batches SET account_id = ? WHERE account_id IS NULL
				AND id IN (SELECT DISTINCT import_batch_id FROM transactions WHERE account_id = ? AND import_batch_id IS NOT NULL)`,
				a.ID, a.ID); err != nil {
				return err
			}
			for _, sid := range ab.SchemaIDs {
				if _, err := tx.Exec(`UPDATE import_schemas SET account_id = ? WHERE id = ? AND account_id IS NULL`, a.ID, sid); err != nil {
					return err
				}
			}
			res.Accounts = append(res.Accounts, RestoredAccount{ID: a.ID, Name: a.Name,
				Transactions: len(ab.Transactions), Valuations: len(ab.Valuations), Transfers: relinked[a.ID]})
			if _, err := s.Audit.Write(tx, audit.Input{
				Actor: core.ActorUser, Action: "account.restored",
				EntityType: "account", EntityID: a.ID,
				Title: "Restored account",
				Sub:   fmt.Sprintf("%s · %s · from %s", a.Name, plural(len(ab.Transactions), "transaction"), source),
				After: map[string]any{"name": a.Name, "transactions": len(ab.Transactions),
					"valuations": len(ab.Valuations), "archive_id": archiveID},
			}); err != nil {
				return err
			}
		}
		if archiveID != "" {
			if _, err := tx.Exec(`DELETE FROM account_archives WHERE id = ?`, archiveID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.afterChange()
	return res, nil
}

func mapCategories(tx *sql.Tx, cats []BackupCategory) (map[string]string, []string, error) {
	out := map[string]string{}
	var created []string
	now := util.NowStr()
	for _, c := range cats {
		var id string
		var deleted sql.NullString
		err := tx.QueryRow(`SELECT id, deleted_at FROM categories WHERE id = ?`, c.ID).Scan(&id, &deleted)
		if err == sql.ErrNoRows {
			err = tx.QueryRow(`SELECT id, deleted_at FROM categories WHERE name = ? COLLATE NOCASE`, c.Name).Scan(&id, &deleted)
		}
		switch {
		case err == nil:
			if deleted.Valid {
				if _, err := tx.Exec(`UPDATE categories SET deleted_at = NULL, updated_at = ? WHERE id = ?`, now, id); err != nil {
					return nil, nil, err
				}
			}
			out[c.ID] = id
		case err == sql.ErrNoRows:
			if _, err := tx.Exec(`INSERT INTO categories (id, name, color, type, created_at, updated_at) VALUES (?,?,?,?,?,?)`,
				c.ID, c.Name, orDefault(c.Color, "#6b7280"), c.Type, now, now); err != nil {
				return nil, nil, err
			}
			out[c.ID] = c.ID
			created = append(created, c.Name)
		default:
			return nil, nil, err
		}
	}
	return out, created, nil
}

func internalCategoryID(q querier) (string, error) {
	var id string
	err := q.QueryRow(`SELECT id FROM categories WHERE name = 'Internal transfer' COLLATE NOCASE AND deleted_at IS NULL`).Scan(&id)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return id, err
}

func legFree(tx *sql.Tx, id string) bool {
	var n int
	_ = tx.QueryRow(`SELECT COUNT(*) FROM transactions WHERE id = ? AND deleted_at IS NULL AND transfer_id IS NULL`, id).Scan(&n)
	return n == 1
}

func categoryExists(tx *sql.Tx, id string) bool {
	var n int
	_ = tx.QueryRow(`SELECT COUNT(*) FROM categories WHERE id = ? AND deleted_at IS NULL`, id).Scan(&n)
	return n == 1
}

func freeID(tx *sql.Tx, table, id string) string {
	var n int
	_ = tx.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE id = ?`, id).Scan(&n)
	if n == 0 && id != "" {
		return id
	}
	return util.NewID()
}

func mapped(m map[string]string, id string) string {
	if v, ok := m[id]; ok {
		return v
	}
	return id
}

func (s *Service) afterChange() {
	if s.AfterChange != nil {
		s.AfterChange()
	}
}

func optStr(n sql.NullString) *string {
	if !n.Valid {
		return nil
	}
	v := n.String
	return &v
}

func ptrVal(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func isDate(s string) bool {
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

func quoteJoin(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = "“" + n + "”"
	}
	return strings.Join(q, ", ")
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}
