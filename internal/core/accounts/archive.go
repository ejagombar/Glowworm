package accounts

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"glowworm/internal/core"
	"glowworm/internal/core/audit"
	"glowworm/internal/store/repos"
	"glowworm/internal/util"
)

type snapshot struct {
	Account      snapAccount       `json:"account"`
	Valuations   []snapValuation   `json:"valuations"`
	Transactions []snapTransaction `json:"transactions"`
	Transfers    []snapTransfer    `json:"transfers"`
	SchemaIDs    []string          `json:"schema_ids,omitempty"`
	Categories   []snapCategory    `json:"categories"`
}

type snapAccount struct {
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

type snapValuation struct {
	ID         string `json:"id"`
	ValueMinor int64  `json:"value_minor"`
	Date       string `json:"date"`
	Note       string `json:"note,omitempty"`
	Source     string `json:"source"`
	DriftMinor *int64 `json:"drift_minor,omitempty"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

type snapTransaction struct {
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

type snapTransfer struct {
	ID                       string   `json:"id"`
	SourceTransactionID      string   `json:"source_transaction_id"`
	DestinationTransactionID string   `json:"destination_transaction_id"`
	Detected                 string   `json:"detected"`
	Method                   string   `json:"method,omitempty"`
	Confidence               *float64 `json:"confidence,omitempty"`
	CreatedAt                string   `json:"created_at"`
}

type snapCategory struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Color       string `json:"color"`
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
}

type Archive struct {
	ID        string `json:"id"`
	AccountID string `json:"account_id"`
	Name      string `json:"name"`
	TxCount   int    `json:"tx_count"`
	DeletedAt string `json:"deleted_at"`
}

type RestoredAccount struct {
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	Transactions      int      `json:"transactions"`
	Valuations        int      `json:"valuations"`
	Transfers         int      `json:"transfers"`
	CategoriesCreated []string `json:"categories_created,omitempty"`
}

type querier interface {
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

func buildSnapshot(q querier, id string) (*snapshot, error) {
	ab := &snapshot{Categories: []snapCategory{}, Valuations: []snapValuation{}, Transactions: []snapTransaction{}, Transfers: []snapTransfer{}}
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
		var v snapValuation
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
		var t snapTransaction
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
		var tr snapTransfer
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
	seen := map[string]bool{}
	for _, t := range ab.Transactions {
		if t.CategoryID == nil || seen[*t.CategoryID] {
			continue
		}
		seen[*t.CategoryID] = true
		var c snapCategory
		err := q.QueryRow(`SELECT id, name, color, type, description FROM categories WHERE id = ?`, *t.CategoryID).
			Scan(&c.ID, &c.Name, &c.Color, &c.Type, &c.Description)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return nil, err
		}
		ab.Categories = append(ab.Categories, c)
	}
	return ab, nil
}

func (s *Service) Delete(id string) (*Archive, error) {
	var arc *Archive
	err := s.DB.WithTx(func(tx *sql.Tx) error {
		snap, err := buildSnapshot(tx, id)
		if err != nil {
			return err
		}
		payload, err := json.Marshal(snap)
		if err != nil {
			return err
		}
		arc = &Archive{ID: util.NewID(), AccountID: id, Name: snap.Account.Name,
			TxCount: len(snap.Transactions), DeletedAt: util.NowStr()}
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
			Sub:   fmt.Sprintf("%s · %s · restorable from Settings", snap.Account.Name, plural(arc.TxCount, "transaction")),
			Before: map[string]any{
				"name": snap.Account.Name, "type": snap.Account.Type, "currency": snap.Account.Currency,
				"transactions": arc.TxCount, "valuations": len(snap.Valuations),
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
		core.InternalTransferCategoryID, core.InternalTransferCategoryID, core.InternalTransferCategoryID, util.NowStr(), id, id, id); err != nil {
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

func (s *Service) loadArchive(archiveID string) (*Archive, *snapshot, error) {
	var a Archive
	var payload string
	err := s.DB.QueryRow(`SELECT id, account_id, name, tx_count, deleted_at, payload FROM account_archives WHERE id = ?`, archiveID).
		Scan(&a.ID, &a.AccountID, &a.Name, &a.TxCount, &a.DeletedAt, &payload)
	if err != nil {
		return nil, nil, err
	}
	var snap snapshot
	if err := json.Unmarshal([]byte(payload), &snap); err != nil {
		return nil, nil, fmt.Errorf("archive %s is unreadable: %w", archiveID, err)
	}
	return &a, &snap, nil
}

func (s *Service) PurgeArchive(archiveID string) error {
	arc, _, err := s.loadArchive(archiveID)
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

func (s *Service) RestoreArchive(archiveID string) (*RestoredAccount, error) {
	_, snap, err := s.loadArchive(archiveID)
	if err != nil {
		return nil, err
	}
	a := snap.Account
	res := &RestoredAccount{ID: a.ID, Name: a.Name, Transactions: len(snap.Transactions), Valuations: len(snap.Valuations)}
	err = s.DB.WithTx(func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM accounts WHERE id = ?`, a.ID).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return &ValidationError{Message: fmt.Sprintf("“%s” already exists", a.Name)}
		}
		catMap, created, err := mapCategories(&repos.Categories{DB: s.DB}, tx, snap.Categories)
		if err != nil {
			return err
		}
		res.CategoriesCreated = created
		now := util.NowStr()
		if _, err := tx.Exec(`INSERT INTO accounts (id, name, type, color, currency, opening_balance_minor,
			opening_balance_date, account_number, sort_code, iban, bic, notes, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			a.ID, a.Name, a.Type, a.Color, a.Currency, a.OpeningBalanceMinor, nullIfEmpty(a.OpeningBalanceDate),
			a.AccountNumber, a.SortCode, a.IBAN, a.BIC, a.Notes, a.CreatedAt, now); err != nil {
			return err
		}
		for _, v := range snap.Valuations {
			var drift any
			if v.DriftMinor != nil {
				drift = *v.DriftMinor
			}
			if _, err := tx.Exec(`INSERT INTO valuations (id, account_id, value_minor, date, note, source, drift_minor, created_at, updated_at)
				VALUES (?,?,?,?,?,?,?,?,?)`, freeID(tx, "valuations", v.ID), a.ID, v.ValueMinor, v.Date, v.Note, v.Source,
				drift, v.CreatedAt, now); err != nil {
				return err
			}
		}
		txIDs := map[string]string{}
		for _, t := range snap.Transactions {
			id := freeID(tx, "transactions", t.ID)
			txIDs[t.ID] = id
			var catID any
			if t.CategoryID != nil {
				if m, ok := catMap[*t.CategoryID]; ok {
					catID = m
				}
			}
			var conf any
			if t.CategoryConfidence != nil {
				conf = *t.CategoryConfidence
			}
			if _, err := tx.Exec(`INSERT INTO transactions (id, account_id, date, description, merchant, amount_minor,
				currency, type, category_id, category_source, category_confidence, source_text, destination_text,
				recurring_key, transfer_id, import_batch_id, dedupe_hash, created_at, updated_at)
				VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,NULL,?,?,?,?)`,
				id, a.ID, t.Date, t.Description, ptrVal(t.Merchant), t.AmountMinor, t.Currency, t.Type,
				catID, t.CategorySource, conf, t.SourceText, t.DestinationText,
				ptrVal(t.RecurringKey), ptrVal(t.ImportBatchID), t.DedupeHash, t.CreatedAt, now); err != nil {
				return err
			}
		}
		for _, tr := range snap.Transfers {
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
				VALUES (?,?,?,?,?,?,?)`, trID, src, dst, tr.Detected, tr.Method, conf, tr.CreatedAt); err != nil {
				return err
			}
			for _, leg := range []string{src, dst} {
				if _, err := tx.Exec(`UPDATE transactions SET type = 'transfer', transfer_id = ?, category_id = ?,
					category_source = 'none', category_confidence = NULL, updated_at = ? WHERE id = ?`,
					trID, core.InternalTransferCategoryID, now, leg); err != nil {
					return err
				}
			}
			res.Transfers++
		}
		// Legs whose other side is gone stay transfers waiting to be paired
		// again, so they stay out of spend and income.
		if _, err := tx.Exec(`UPDATE transactions SET category_id = ?, category_source = 'none'
			WHERE account_id = ? AND type = 'transfer' AND transfer_id IS NULL`, core.InternalTransferCategoryID, a.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE import_batches SET account_id = ? WHERE account_id IS NULL
			AND id IN (SELECT DISTINCT import_batch_id FROM transactions WHERE account_id = ? AND import_batch_id IS NOT NULL)`,
			a.ID, a.ID); err != nil {
			return err
		}
		for _, sid := range snap.SchemaIDs {
			if _, err := tx.Exec(`UPDATE import_schemas SET account_id = ? WHERE id = ? AND account_id IS NULL`, a.ID, sid); err != nil {
				return err
			}
		}
		if _, err := s.Audit.Write(tx, audit.Input{
			Actor: core.ActorUser, Action: "account.restored",
			EntityType: "account", EntityID: a.ID,
			Title: "Restored account",
			Sub:   fmt.Sprintf("%s · %s", a.Name, plural(len(snap.Transactions), "transaction")),
			After: map[string]any{"name": a.Name, "transactions": len(snap.Transactions),
				"valuations": len(snap.Valuations), "archive_id": archiveID},
		}); err != nil {
			return err
		}
		_, err = tx.Exec(`DELETE FROM account_archives WHERE id = ?`, archiveID)
		return err
	})
	if err != nil {
		return nil, err
	}
	s.afterChange()
	return res, nil
}

func mapCategories(repo *repos.Categories, tx *sql.Tx, cats []snapCategory) (map[string]string, []string, error) {
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
		switch err {
		case nil:
			if deleted.Valid {
				// It comes back, unless another category has its name now:
				// then its transactions go to that one.
				_, by, err := repo.Namesake(tx, id)
				if err != nil {
					return nil, nil, err
				}
				if by != "" {
					id = by
				} else if err := repo.Restore(tx, id); err != nil {
					return nil, nil, err
				}
			}
			out[c.ID] = id
		case sql.ErrNoRows:
			if _, err := tx.Exec(`INSERT INTO categories (id, name, color, type, description, created_at, updated_at) VALUES (?,?,?,?,?,?,?)`,
				c.ID, c.Name, c.Color, c.Type, c.Description, now, now); err != nil {
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

func legFree(tx *sql.Tx, id string) bool {
	var n int
	_ = tx.QueryRow(`SELECT COUNT(*) FROM transactions WHERE id = ? AND deleted_at IS NULL AND transfer_id IS NULL`, id).Scan(&n)
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

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}
