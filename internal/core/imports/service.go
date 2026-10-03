// Package imports implements the import pipeline (doc 06): upload → sniff →
// schema match/detect → parse+stage → review → commit (+detections+retrain).
package imports

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"glowworm/internal/ai"
	"glowworm/internal/core"
	"glowworm/internal/core/audit"
	"glowworm/internal/core/categoriser"
	"glowworm/internal/importer"
	"glowworm/internal/store"
	"glowworm/internal/store/repos"
	"glowworm/internal/util"
)

// Service owns the pipeline.
type Service struct {
	DB          *store.DB
	Accounts    *repos.Accounts
	Txs         *repos.Transactions
	Audit       *audit.Writer
	Cat         *categoriser.Service
	Provider    ai.Provider // may be nil (AI disabled)
	DisplayCC   string
	AfterCommit func(batchID string) // detection hooks (transfers, recurring, retrain)
}

// ValidationError is user-facing.
type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

// ErrDuplicateFile per doc 06 §8.
var ErrDuplicateFile = errors.New("this exact file was already imported")

// Batch is a view-model of import_batches (+ staged row count).
type Batch struct {
	*core.ImportBatch
	Rows       int        `json:"rows"`
	SchemaName string     `json:"schema_name,omitempty"`
	Sheets     []string   `json:"sheets,omitempty"`
	Detection  *Detection `json:"detection,omitempty"`
}

// Detection is the detector output shown in the schema preview (doc 06 §5).
type Detection struct {
	Kind          string               `json:"kind"`
	Sheet         string               `json:"sheet,omitempty"`
	Sheets        []string             `json:"sheets,omitempty"`
	HasHeader     bool                 `json:"has_header"`
	Delimiter     string               `json:"delimiter,omitempty"`
	Columns       []importer.ColumnMap `json:"columns"`
	DateFormat    string               `json:"date_format,omitempty"`
	BankHint      string               `json:"bank_hint,omitempty"`
	ProposedName  string               `json:"proposed_name,omitempty"`
	Confidence    float64              `json:"confidence"`
	MatchedSchema string               `json:"matched_schema,omitempty"`
	Sample        [][]string           `json:"sample,omitempty"`
}

// Upload stores the file and creates the batch (doc 06 §1). The original
// bytes are retained in import_files so the fix-mapping stage can re-parse.
func (s *Service) Upload(filename string, data []byte, accountID *string) (*Batch, error) {
	if len(data) == 0 {
		return nil, &ValidationError{Message: "That file is empty."}
	}
	kind := importer.Sniff(filename, data)
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])

	// Duplicate file check (doc 06 §8).
	var prior string
	err := s.DB.QueryRow(`SELECT status FROM import_batches WHERE sha256 = ? AND status = 'confirmed' LIMIT 1`, sha).Scan(&prior)
	if err == nil {
		return nil, ErrDuplicateFile
	}

	batch := &core.ImportBatch{
		Filename: filename, SizeBytes: int64(len(data)), SHA256: sha,
		Mime: string(kind), AccountID: accountID, Status: "detecting",
	}
	var b *Batch
	err = s.DB.WithTx(func(tx *sql.Tx) error {
		batch.ID = util.NewID()
		batch.CreatedAt = util.NowStr()
		if _, err := tx.Exec(`INSERT INTO import_batches (id, filename, size_bytes, sha256, mime, account_id, status, created_at)
			VALUES (?,?,?,?,?,?,?,?)`,
			batch.ID, batch.Filename, batch.SizeBytes, batch.SHA256, batch.Mime,
			nullStr(accountID), batch.Status, batch.CreatedAt); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO import_files (batch_id, content) VALUES (?,?)`, batch.ID, data); err != nil {
			return err
		}
		_, err := s.Audit.Write(tx, audit.Input{
			Actor: core.ActorUser, Action: "import.created",
			EntityType: "import_batch", EntityID: batch.ID,
			Title: "Uploaded statement", Sub: filename,
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	b, err = s.Detect(batch.ID, kind, filename, data)
	if err != nil {
		return b, err
	}
	return b, nil
}

// DetectionRows caps the sample lines sent to the LLM (doc 09 §10).
const detectionSampleLines = 5

// Detect runs the detection order (doc 06 §5.1): saved schema fingerprint →
// LLM → manual-ready auto mapping.
func (s *Service) Detect(batchID string, kind importer.Kind, filename string, data []byte) (*Batch, error) {
	if _, err := s.getBatch(batchID); err != nil {
		return nil, err
	}
	det := &Detection{Kind: string(kind)}
	var rows []importer.Row

	switch kind {
	case importer.KindCSV:
		table, delim, err := importer.ReadCSV(data)
		if err != nil {
			return s.markFailed(batchID, "Couldn't read the file as CSV.")
		}
		det.Delimiter = delim
		if len(table) > 0 {
			det.HasHeader = true
			det.Sample = sampleTable(table, detectionSampleLines+1)
		}
		// Saved schema fingerprint (doc 06 §5.1 step 1).
		if m, schema := s.matchSavedSchema(kind, table); schema != nil {
			det.Columns = m.Columns
			det.Confidence = 1.0
			det.ProposedName = schema.Name
			det.MatchedSchema = schema.Name
			det.BankHint = schema.BankHint
			det.DateFormat = m.DateFormat
			det.Columns = m.Columns
			det.Confidence = 1.0
			det.ProposedName = schema.Name
			det.MatchedSchema = schema.Name
			det.BankHint = schema.BankHint
			b, err := s.applyMapping(batchID, m, data, &schema.ID, det)
			if err != nil {
				return nil, err
			}
			return b, nil
		}
		// LLM detection (doc 06 §5.1 step 2).
		if s.Provider != nil && len(table) > 1 {
			m, err := s.detectWithAI(kind, table)
			if err == nil {
				det.Columns = m.Columns
				det.HasHeader = m.HasHeader
				det.Delimiter = m.Delimiter
				det.ProposedName = m.ProposedName
				det.Confidence = m.Confidence
				det.BankHint = m.BankHint
				det.DateFormat = m.DateFormat
				b, err := s.applyMapping(batchID, m, data, nil, det)
				if err != nil {
					return nil, err
				}
				return b, nil
			}
		}
		// Manual-ready auto mapping (doc 06 §5.1 step 3).
		if len(table) > 0 {
			m := importer.AutoMapping(kind, table[0], nil)
			det.Columns = m.Columns
			det.HasHeader = true
			det.Delimiter = delim
			det.Confidence = 0.5
			_ = rows
		}
	case importer.KindXLSX:
		sheets, table, err := importer.ReadXLSX(data, "")
		if err != nil {
			return s.markFailed(batchID, err.Error())
		}
		det.Sheets = sheets
		if len(table) > 0 {
			det.HasHeader = true
			det.Sample = sampleTable(table, detectionSampleLines+1)
			m := importer.AutoMapping(kind, table[0], nil)
			det.Columns = m.Columns
			det.Confidence = 0.5
		}
	case importer.KindQIF:
		rr, err := importer.ReadQIF(data, s.DisplayCC)
		if err != nil {
			return s.markFailed(batchID, err.Error())
		}
		rows = rr
	case importer.KindOFX:
		rr, err := importer.ReadOFX(data, s.DisplayCC)
		if err != nil {
			return s.markFailed(batchID, err.Error())
		}
		rows = rr
	case importer.KindPDF:
		text, err := importer.ExtractPDFText(data)
		if err != nil {
			return s.markFailed(batchID, "No text layer in that PDF (scanned PDFs need the optional vision path).")
		}
		det.Sample = sampleLines(strings.Split(text, "\n"), detectionSampleLines)
		if err := s.markAwaitingSchema(batchID, det); err != nil {
			return nil, err
		}
		return s.Get(batchID)
	}

	// QIF/OFX parse directly to rows — stage immediately.
	if kind == importer.KindQIF || kind == importer.KindOFX {
		if _, err := s.stageRows(batchID, rows); err != nil {
			return nil, err
		}
		if err := s.setStatus(batchID, "awaiting_review"); err != nil {
			return nil, err
		}
		return s.Get(batchID)
	}
	if err := s.markAwaitingSchema(batchID, det); err != nil {
		return nil, err
	}
	return s.Get(batchID)
}

// detectWithAI calls the schema_detection capability (doc 09 §6.1) and
// validates the mapping deterministically (doc 09 §6.1: Go verifies).
func (s *Service) detectWithAI(kind importer.Kind, table [][]string) (*importer.Mapping, error) {
	headers := table[0]
	sample := table[1:]
	if len(sample) > detectionSampleLines {
		sample = sample[:detectionSampleLines]
	}
	resp, _, err := s.Provider.CompleteStructured(&ai.StructuredRequest{
		Capability: "schema_detection",
		PromptJSON: map[string]any{
			"format":      string(kind),
			"columns":     headers,
			"sample_rows": sample,
		},
	})
	if err != nil {
		return nil, err
	}
	var m importer.Mapping
	if err := json.Unmarshal([]byte(resp), &m); err != nil {
		return nil, err
	}
	if err := validateMapping(&m, headers, sample); err != nil {
		return nil, err
	}
	return &m, nil
}

// validateMapping checks roles/dates deterministically (doc 09 §6.1).
func validateMapping(m *importer.Mapping, headers []string, sample [][]string) error {
	hasDate, hasAmount := false, false
	known := map[string]bool{}
	for _, h := range headers {
		known[strings.TrimSpace(h)] = true
	}
	for _, c := range m.Columns {
		if !known[c.Source] {
			return fmt.Errorf("column %q is not in the file", c.Source)
		}
		switch c.Role {
		case importer.RoleDate:
			hasDate = true
		case importer.RoleAmount, importer.RoleAmountIn, importer.RoleAmountOut:
			hasAmount = true
		case importer.RoleDesc, importer.RoleCurrency, importer.RoleBalance,
			importer.RoleTypeHint, importer.RoleMemo, importer.RoleSource, importer.RoleDestination, importer.RoleIgnore:
		default:
			return fmt.Errorf("unknown role %q", c.Role)
		}
	}
	if !hasDate || !hasAmount {
		return fmt.Errorf("mapping needs a date column and an amount column")
	}
	return nil
}

// applyMapping parses + stages with the given mapping (doc 06 §6) and saves
// the schema fingerprint when schemaID is provided.
func (s *Service) applyMapping(batchID string, m *importer.Mapping, data []byte, schemaID *string, det *Detection) (*Batch, error) {
	batch, err := s.getBatch(batchID)
	if err != nil {
		return nil, err
	}
	kind := importer.Kind(m.Kind)
	if kind == "" {
		kind = importer.Kind(batch.Mime)
	}
	var rows []importer.Row
	switch kind {
	case importer.KindXLSX:
		_, table, err := importer.ReadXLSX(data, m.Sheet)
		if err != nil {
			return nil, err
		}
		rows, err = importer.ParseRows(m, table, s.DisplayCC)
		if err != nil {
			return s.markParseError(batchID, err)
		}
	case importer.KindCSV:
		table, _, err := importer.ReadCSV(data)
		if err != nil {
			return nil, err
		}
		rows, err = importer.ParseRows(m, table, s.DisplayCC)
		if err != nil {
			return s.markParseError(batchID, err)
		}
	default:
		return nil, &ValidationError{Message: "Mapping applies only to CSV and Excel files"}
	}
	if len(rows) == 0 {
		return s.markParseError(batchID, &importer.ParseError{Message: "no rows parsed — check the column mapping"})
	}
	// Save/refresh the schema fingerprint on confirm; for now record schema_id.
	if schemaID != nil {
		if _, err := s.DB.Exec(`UPDATE import_batches SET schema_id = ? WHERE id = ?`, *schemaID, batchID); err != nil {
			return nil, err
		}
	}
	if _, err := s.stageRows(batchID, rows); err != nil {
		return nil, err
	}
	if err := s.setStatus(batchID, "awaiting_review"); err != nil {
		return nil, err
	}
	return s.Get(batchID)
}

// stageRows inserts parsed rows into import_staging with dedupe + cascade
// categorisation (doc 06 §6).
func (s *Service) stageRows(batchID string, rows []importer.Row) (int, error) {
	var count int
	err := s.DB.WithTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM import_staging WHERE batch_id = ?`, batchID); err != nil {
			return err
		}
		staged := make([]*core.Transaction, 0, len(rows))
		stagingIDs := []string{}
		for _, r := range rows {
			stagingID := util.NewID()
			stagingIDs = append(stagingIDs, stagingID)
			hash := repos.DedupeHash(deref(s.batchAccountID(tx, batchID)), r.Date, r.AmountMinor, r.Description)
			if r.FitID != "" {
				hash = "fitid:" + r.FitID
			}
			var dup any
			if r.FitID != "" {
				var existing string
				err := tx.QueryRow(`SELECT id FROM transactions WHERE dedupe_hash = ? AND deleted_at IS NULL LIMIT 1`, hash).Scan(&existing)
				if err == nil {
					dup = existing
				}
			} else {
				var existing string
				err := tx.QueryRow(`SELECT id FROM transactions WHERE account_id = ? AND dedupe_hash = ? AND deleted_at IS NULL LIMIT 1`,
					deref(s.batchAccountID(tx, batchID)), hash).Scan(&existing)
				if err == nil {
					dup = existing
				}
			}
			t := &core.Transaction{
				ID: stagingID, Date: r.Date, Description: r.Description,
				AmountMinor: r.AmountMinor, Currency: r.Currency,
				SourceText: r.SourceText, DestinationText: r.DestinationText,
				DedupeHash: hash, CategorySource: core.CategorySourceNone,
			}
			if r.AmountMinor >= 0 {
				t.Type = core.TypeIncome
			} else {
				t.Type = core.TypeExpense
			}
			staged = append(staged, t)
			raw, _ := json.Marshal(r.Raw)
			if _, err := tx.Exec(`INSERT INTO import_staging
				(id, batch_id, line_no, date, description, amount_minor, currency, source_text, destination_text, duplicate_of, raw, created_at)
				VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
				stagingID, batchID, r.Line, r.Date, r.Description, r.AmountMinor, r.Currency,
				r.SourceText, r.DestinationText, dup, string(raw), util.NowStr()); err != nil {
				return err
			}
			count++
		}
		// Categorisation cascade over staged rows (doc 06 §6 last bullet).
		if s.Cat != nil {
			if _, err := s.Cat.Categorise(tx, staged); err != nil {
				return err
			}
			for i, t := range staged {
				_, err := tx.Exec(`UPDATE import_staging SET proposed_category_id = ?, category_source = ?, category_confidence = ?, needs_review = ?
					WHERE id = ?`, nullStr(t.CategoryID), t.CategorySource, nullConf(t.CategoryConfidence),
					boolToInt(t.CategorySource == core.CategorySourceNone), stagingIDs[i])
				if err != nil {
					return err
				}
			}
		}
		return nil
	})
	return count, err
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullConf(f *float64) any {
	if f == nil {
		return nil
	}
	return *f
}

func (s *Service) batchAccountID(tx *sql.Tx, batchID string) *string {
	var accountID any
	_ = tx.QueryRow(`SELECT account_id FROM import_batches WHERE id = ?`, batchID).Scan(&accountID)
	if accountID == nil {
		return nil
	}
	str := accountID.(string)
	return &str
}

// Get returns the batch view-model.
func (s *Service) Get(batchID string) (*Batch, error) {
	batch, err := s.getBatch(batchID)
	if err != nil {
		return nil, err
	}
	b := &Batch{ImportBatch: batch}
	var rows int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM import_staging WHERE batch_id = ?`, batchID).Scan(&rows)
	b.Rows = rows
	if batch.SchemaID != nil {
		var name string
		if err := s.DB.QueryRow(`SELECT name FROM import_schemas WHERE id = ?`, *batch.SchemaID).Scan(&name); err == nil {
			b.SchemaName = name
		}
	}
	if batch.Detection != nil {
		var d Detection
		if err := json.Unmarshal([]byte(*batch.Detection), &d); err == nil {
			b.Detection = &d
			b.Sheets = d.Sheets
		}
	}
	return b, nil
}

func (s *Service) getBatch(id string) (*core.ImportBatch, error) {
	var b core.ImportBatch
	var accountID, schemaID, detection, parseErr, confirmedAt, createdAt sql.NullString
	err := s.DB.QueryRow(`SELECT id, filename, size_bytes, sha256, mime, account_id, schema_id, status, detection, parse_error, created_at, confirmed_at
		FROM import_batches WHERE id = ?`, id).
		Scan(&b.ID, &b.Filename, &b.SizeBytes, &b.SHA256, &b.Mime, &accountID, &schemaID, &b.Status, &detection, &parseErr, &createdAt, &confirmedAt)
	if err != nil {
		return nil, err
	}
	if accountID.Valid {
		s := accountID.String
		b.AccountID = &s
	}
	if schemaID.Valid {
		s := schemaID.String
		b.SchemaID = &s
	}
	if detection.Valid {
		s := detection.String
		b.Detection = &s
	}
	if parseErr.Valid {
		s := parseErr.String
		b.ParseError = &s
	}
	if confirmedAt.Valid {
		s := confirmedAt.String
		b.ConfirmedAt = &s
	}
	b.CreatedAt = createdAt.String
	return &b, nil
}

func (s *Service) setStatus(id, status string) error {
	_, err := s.DB.Exec(`UPDATE import_batches SET status = ? WHERE id = ?`, status, id)
	return err
}

func (s *Service) markFailed(id, msg string) (*Batch, error) {
	_ = s.setStatus(id, "failed")
	if _, err := s.DB.Exec(`UPDATE import_batches SET parse_error = ? WHERE id = ?`, msg, id); err != nil {
		return nil, err
	}
	return s.Get(id)
}

func (s *Service) markParseError(id string, err error) (*Batch, error) {
	msg := err.Error()
	if pe, ok := err.(*importer.ParseError); ok {
		msg = fmt.Sprintf("Only some rows parsed — check the column mapping (%s)", pe.Error())
	}
	_ = s.setStatus(id, "awaiting_schema")
	if _, err := s.DB.Exec(`UPDATE import_batches SET parse_error = ? WHERE id = ?`, msg, id); err != nil {
		return nil, err
	}
	return s.Get(id)
}

func (s *Service) markAwaitingSchema(id string, det *Detection) error {
	b, _ := json.Marshal(det)
	_, err := s.DB.Exec(`UPDATE import_batches SET detection = ?, status = 'awaiting_schema' WHERE id = ?`, string(b), id)
	return err
}

// ApplySchemaPublic re-parses the stored upload with a corrected mapping
// (doc 06 §5 fix-mapping stage).
func (s *Service) ApplySchemaPublic(batchID string, m *importer.Mapping, data []byte) (*Batch, error) {
	return s.applyMapping(batchID, m, data, nil, nil)
}

func sampleTable(table [][]string, n int) [][]string {
	if len(table) > n {
		return table[:n]
	}
	return table
}

func sampleLines(lines []string, n int) [][]string {
	out := [][]string{}
	for i, l := range lines {
		if i >= n {
			break
		}
		out = append(out, strings.Split(l, ","))
	}
	return out
}

// matchSavedSchema finds a saved schema by fingerprint (doc 06 §5.1).
func (s *Service) matchSavedSchema(kind importer.Kind, table [][]string) (*importer.Mapping, *core.ImportSchema) {
	if len(table) == 0 {
		return nil, nil
	}
	h := schemaHash(kind, table[0])
	var id, name, bank, mappingJSON string
	err := s.DB.QueryRow(`SELECT id, name, bank_hint, mapping FROM import_schemas WHERE sample_hash = ?`, h).
		Scan(&id, &name, &bank, &mappingJSON)
	if err != nil {
		return nil, nil
	}
	var m importer.Mapping
	if err := json.Unmarshal([]byte(mappingJSON), &m); err != nil {
		return nil, nil
	}
	return &m, &core.ImportSchema{ID: id, Name: name, BankHint: bank}
}

func schemaHash(kind importer.Kind, header []string) string {
	h := sha256.Sum256([]byte(string(kind) + "|" + strings.Join(normHeaders(header), "|")))
	return hex.EncodeToString(h[:8])
}

func normHeaders(header []string) []string {
	out := make([]string, len(header))
	for i, h := range header {
		out[i] = strings.ToLower(strings.TrimSpace(h))
	}
	return out
}

// StagedRow is the review-table projection (doc 06 §7).
type StagedRow struct {
	ID             string   `json:"id"`
	Line           int      `json:"line_no"`
	Date           string   `json:"date"`
	Description    string   `json:"description"`
	AmountMinor    int64    `json:"amount_minor"`
	Currency       string   `json:"currency"`
	CategoryID     *string  `json:"category_id"`
	CategoryName   string   `json:"category_name"`
	CategorySource string   `json:"category_source"`
	Confidence     *float64 `json:"category_confidence"`
	NeedsReview    bool     `json:"needs_review"`
	DuplicateOf    *string  `json:"duplicate_of,omitempty"`
}

// Review returns staged rows + aggregates (doc 03 §7).
func (s *Service) Review(batchID string) (rows []*StagedRow, confident, needsReview, duplicates int, err error) {
	brows, e := s.DB.Query(`SELECT st.id, st.line_no, st.date, st.description, st.amount_minor, st.currency,
		st.proposed_category_id, COALESCE(c.name,''), st.category_source, st.category_confidence, st.needs_review, st.duplicate_of
		FROM import_staging st LEFT JOIN categories c ON c.id = st.proposed_category_id
		WHERE st.batch_id = ? ORDER BY st.line_no ASC`, batchID)
	if e != nil {
		return nil, 0, 0, 0, e
	}
	defer brows.Close()
	for brows.Next() {
		var r StagedRow
		var catID, dup sql.NullString
		var conf sql.NullFloat64
		var needs int
		if e := brows.Scan(&r.ID, &r.Line, &r.Date, &r.Description, &r.AmountMinor, &r.Currency,
			&catID, &r.CategoryName, &r.CategorySource, &conf, &needs, &dup); e != nil {
			return nil, 0, 0, 0, e
		}
		if catID.Valid {
			s := catID.String
			r.CategoryID = &s
		}
		if conf.Valid {
			f := conf.Float64
			r.Confidence = &f
		}
		if dup.Valid {
			s := dup.String
			r.DuplicateOf = &s
			duplicates++
		}
		r.NeedsReview = needs == 1
		if r.NeedsReview {
			needsReview++
		} else {
			confident++
		}
		rows = append(rows, &r)
	}
	return rows, confident, needsReview, duplicates, brows.Err()
}

// ReviewPatch applies bulk ops (doc 03 §7).
type ReviewPatch struct {
	SetCategory *struct {
		RowIDs     []string `json:"row_ids"`
		CategoryID string   `json:"category_id"`
	} `json:"set_category,omitempty"`
	AcceptConfident bool     `json:"accept_confident"`
	ForceImport     []string `json:"force_import,omitempty"`
}

// ApplyReview patches staged rows.
func (s *Service) ApplyReview(batchID string, p ReviewPatch) error {
	return s.DB.WithTx(func(tx *sql.Tx) error {
		if p.SetCategory != nil {
			for _, rowID := range p.SetCategory.RowIDs {
				if _, err := tx.Exec(`UPDATE import_staging SET proposed_category_id = ?, category_source = 'user',
					category_confidence = NULL, needs_review = 0 WHERE id = ? AND batch_id = ?`,
					p.SetCategory.CategoryID, rowID, batchID); err != nil {
					return err
				}
			}
		}
		if p.ForceImport != nil {
			for _, rowID := range p.ForceImport {
				if _, err := tx.Exec(`UPDATE import_staging SET duplicate_of = NULL WHERE id = ? AND batch_id = ?`,
					rowID, batchID); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// Confirm commits the batch (doc 06 §9): one transaction, one audit entry.
func (s *Service) Confirm(batchID string, accountID string) (*Batch, error) {
	batch, err := s.getBatch(batchID)
	if err != nil {
		return nil, err
	}
	if batch.Status != "awaiting_review" {
		return nil, &ValidationError{Message: "Import is not ready for review"}
	}
	err = s.DB.WithTx(func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT id, line_no, date, description, amount_minor, currency, source_text, destination_text, proposed_category_id, duplicate_of, raw
			FROM import_staging WHERE batch_id = ? ORDER BY line_no ASC`, batchID)
		if err != nil {
			return err
		}
		type stagedRow struct {
			id                             string
			line                           int
			date, desc                     string
			amount                         int64
			currency, sourceText, destText string
			categoryID                     *string
			duplicateOf                    *string
			raw                            string
		}
		var rowsData []stagedRow
		for rows.Next() {
			var r stagedRow
			var catID, dup sql.NullString
			if err := rows.Scan(&r.id, &r.line, &r.date, &r.desc, &r.amount, &r.currency, &r.sourceText, &r.destText, &catID, &dup, &r.raw); err != nil {
				rows.Close()
				return err
			}
			if catID.Valid {
				s := catID.String
				r.categoryID = &s
			}
			if dup.Valid {
				s := dup.String
				r.duplicateOf = &s
			}
			rowsData = append(rowsData, r)
		}
		rows.Close()

		imported := 0
		perCategory := map[string]int{}
		var firstDate, lastDate string
		for _, r := range rowsData {
			if r.duplicateOf != nil {
				continue // duplicates skipped by default (doc 06 §7)
			}
			t := &core.Transaction{
				AccountID: accountID, Date: r.date, Description: r.desc,
				AmountMinor: r.amount, Currency: r.currency,
				Type: typeFor(r.amount), CategoryID: r.categoryID,
				CategorySource: core.CategorySourceUser,
				SourceText:     r.sourceText, DestinationText: r.destText,
				ImportBatchID: &batchID, DedupeHash: repos.DedupeHash(accountID, r.date, r.amount, r.desc),
			}
			if r.categoryID == nil {
				t.CategorySource = core.CategorySourceNone
			}
			if err := s.Txs.Create(tx, t); err != nil {
				return err
			}
			imported++
			if r.categoryID != nil {
				var name string
				_ = tx.QueryRow(`SELECT name FROM categories WHERE id = ?`, *r.categoryID).Scan(&name)
				perCategory[name]++
			}
			if firstDate == "" || r.date < firstDate {
				firstDate = r.date
			}
			if r.date > lastDate {
				lastDate = r.date
			}
		}
		if imported == 0 {
			return &ValidationError{Message: "Nothing to import — all rows are duplicates"}
		}
		// Audit entry with per-category diff (prototype History parity).
		diff := []audit.DiffLine{}
		for name, n := range perCategory {
			diff = append(diff, audit.Add(fmt.Sprintf("%d → %s", n, name)))
		}
		if _, err := tx.Exec(`UPDATE import_batches SET status='confirmed', account_id=?, confirmed_at=? WHERE id=?`,
			accountID, util.NowStr(), batchID); err != nil {
			return err
		}
		_, err = s.Audit.Write(tx, audit.Input{
			Actor: core.ActorUser, Action: "import.confirmed",
			EntityType: "import_batch", EntityID: batchID,
			Title:  fmt.Sprintf("Imported %d transactions", imported),
			Sub:    fmt.Sprintf("%s · %s → %s", batch.Filename, firstDate, lastDate),
			Diff:   diff,
			After:  map[string]any{"batch_id": batchID, "imported": imported},
			Revert: map[string]any{"batch_id": batchID},
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	// Post-commit hooks (doc 06 §9): detections + retrain + merchant
	// enrichment (doc 09 §6.4).
	if s.AfterCommit != nil {
		s.AfterCommit(batchID)
	}
	s.enrichMerchants(batchID)
	return s.Get(batchID)
}

// enrichMerchants calls the optional merchant_extraction capability for
// imported transactions without a merchant (doc 09 §6.4). Best-effort.
func (s *Service) enrichMerchants(batchID string) {
	if s.Provider == nil {
		return
	}
	rows, err := s.DB.Query(`SELECT id, description FROM transactions
		WHERE import_batch_id = ? AND deleted_at IS NULL AND (merchant IS NULL OR merchant = '') LIMIT 20`, batchID)
	if err != nil {
		return
	}
	type item struct {
		Index       int    `json:"index"`
		Description string `json:"description"`
	}
	var items []item
	ids := []string{}
	for rows.Next() {
		var id, desc string
		if err := rows.Scan(&id, &desc); err != nil {
			break
		}
		items = append(items, item{Index: len(items), Description: desc})
		ids = append(ids, id)
	}
	rows.Close()
	if len(items) == 0 {
		return
	}
	resp, _, err := s.Provider.CompleteStructured(&ai.StructuredRequest{
		Capability: "merchant_extraction",
		PromptJSON: map[string]any{"descriptions": items},
	})
	if err != nil {
		return // degrade silently (doc 09 §9)
	}
	var parsed struct {
		Items []struct {
			Index      int     `json:"index"`
			Merchant   string  `json:"merchant"`
			Confidence float64 `json:"confidence"`
		} `json:"items"`
	}
	if json.Unmarshal([]byte(resp), &parsed) != nil {
		return
	}
	_ = s.DB.WithTx(func(tx *sql.Tx) error {
		diff := []audit.DiffLine{}
		for _, it := range parsed.Items {
			if it.Index < 0 || it.Index >= len(ids) || strings.TrimSpace(it.Merchant) == "" || it.Confidence < 0.7 {
				continue
			}
			if _, err := tx.Exec(`UPDATE transactions SET merchant = ? WHERE id = ?`, it.Merchant, ids[it.Index]); err != nil {
				return err
			}
			diff = append(diff, audit.Add(items[it.Index].Description+" → "+it.Merchant))
		}
		if len(diff) > 0 {
			_, err := s.Audit.Write(tx, audit.Input{
				Actor: core.ActorAI, Action: "merchant.enriched",
				EntityType: "import_batch", EntityID: batchID,
				Title: "Detected merchants", Sub: fmt.Sprintf("%d enriched", len(diff)),
				Diff: diff,
			})
			return err
		}
		return nil
	})
}

func typeFor(amount int64) string {
	if amount >= 0 {
		return core.TypeIncome
	}
	return core.TypeExpense
}

// Cancel discards the batch (audit import.cancelled).
func (s *Service) Cancel(batchID string) error {
	batch, err := s.getBatch(batchID)
	if err != nil {
		return err
	}
	return s.DB.WithTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE import_batches SET status='cancelled' WHERE id=?`, batchID); err != nil {
			return err
		}
		_, err := s.Audit.Write(tx, audit.Input{
			Actor: core.ActorUser, Action: "import.cancelled",
			EntityType: "import_batch", EntityID: batchID,
			Title: "Cancelled import", Sub: batch.Filename,
		})
		return err
	})
}

// Schemas CRUD (doc 03 §7).

// SaveSchema persists the confirmed mapping (AI name pre-filled, doc 06 §5.3).
func (s *Service) SaveSchema(name, bankHint string, m *importer.Mapping, sampleHeader []string, confidence float64) (*core.ImportSchema, error) {
	if strings.TrimSpace(name) == "" {
		return nil, &ValidationError{Message: "Schema name is required"}
	}
	mappingJSON, _ := json.Marshal(m)
	cols := make([]string, 0, len(m.Columns))
	for _, c := range m.Columns {
		cols = append(cols, c.Source)
	}
	schema := &core.ImportSchema{
		Name: name, BankHint: bankHint, Kind: string(m.Kind),
		Mapping: string(mappingJSON), Columns: cols,
		SampleHash: schemaHash(m.Kind, sampleHeader), Confidence: confidence,
	}
	err := s.DB.WithTx(func(tx *sql.Tx) error {
		schema.ID = util.NewID()
		schema.CreatedAt = util.NowStr()
		schema.UpdatedAt = schema.CreatedAt
		var existing string
		err := tx.QueryRow(`SELECT id FROM import_schemas WHERE sample_hash = ?`, schema.SampleHash).Scan(&existing)
		if err == nil {
			_, err = tx.Exec(`UPDATE import_schemas SET name=?, bank_hint=?, mapping=?, columns=?, confidence=?, updated_at=? WHERE id=?`,
				schema.Name, schema.BankHint, schema.Mapping, strings.Join(schema.Columns, ","), schema.Confidence, schema.UpdatedAt, existing)
			schema.ID = existing
			return err
		}
		_, err = tx.Exec(`INSERT INTO import_schemas (id, name, bank_hint, kind, mapping, columns, sample_hash, confidence, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?)`,
			schema.ID, schema.Name, schema.BankHint, schema.Kind, schema.Mapping,
			strings.Join(schema.Columns, ","), schema.SampleHash, schema.Confidence, schema.CreatedAt, schema.UpdatedAt)
		if err != nil {
			return err
		}
		_, err = s.Audit.Write(tx, audit.Input{
			Actor: core.ActorUser, Action: "schema.saved",
			EntityType: "import_schema", EntityID: schema.ID,
			Title: fmt.Sprintf("Saved schema “%s”", schema.Name),
			Sub:   fmt.Sprintf("%d columns · %.0f%% match", len(schema.Columns), schema.Confidence*100),
			After: map[string]any{"name": schema.Name, "kind": schema.Kind, "columns": schema.Columns},
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	return schema, nil
}

// ListSchemas returns saved schemas.
func (s *Service) ListSchemas() ([]*core.ImportSchema, error) {
	rows, err := s.DB.Query(`SELECT id, name, bank_hint, kind, mapping, columns, sample_hash, confidence, use_count, last_used_at, created_at, updated_at
		FROM import_schemas ORDER BY use_count DESC, created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*core.ImportSchema
	for rows.Next() {
		var sch core.ImportSchema
		var mapping, cols, lastUsed sql.NullString
		if err := rows.Scan(&sch.ID, &sch.Name, &sch.BankHint, &sch.Kind, &mapping, &cols, &sch.SampleHash, &sch.Confidence, &sch.UseCount, &lastUsed, &sch.CreatedAt, &sch.UpdatedAt); err != nil {
			return nil, err
		}
		if cols.Valid && cols.String != "" {
			sch.Columns = strings.Split(cols.String, ",")
		}
		if lastUsed.Valid {
			s := lastUsed.String
			sch.LastUsedAt = &s
		}
		out = append(out, &sch)
	}
	return out, rows.Err()
}

// DeleteSchema removes a saved schema (audit schema.deleted).
func (s *Service) DeleteSchema(id string) error {
	return s.DB.WithTx(func(tx *sql.Tx) error {
		var name string
		_ = tx.QueryRow(`SELECT name FROM import_schemas WHERE id = ?`, id).Scan(&name)
		if _, err := tx.Exec(`DELETE FROM import_schemas WHERE id = ?`, id); err != nil {
			return err
		}
		_, err := s.Audit.Write(tx, audit.Input{
			Actor: core.ActorUser, Action: "schema.deleted",
			EntityType: "import_schema", EntityID: id,
			Title: fmt.Sprintf("Deleted schema “%s”", name),
		})
		return err
	})
}

func nullStr(s *string) any {
	if s == nil || *s == "" {
		return nil
	}
	return *s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
