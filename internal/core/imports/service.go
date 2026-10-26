package imports

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"

	"glowworm/internal/ai"
	"glowworm/internal/core"
	"glowworm/internal/core/audit"
	"glowworm/internal/core/categoriser"
	"glowworm/internal/fx"
	"glowworm/internal/importer"
	"glowworm/internal/store"
	"glowworm/internal/store/repos"
	"glowworm/internal/util"
)

type Service struct {
	DB          *store.DB
	Accounts    *repos.Accounts
	Txs         *repos.Transactions
	Audit       *audit.Writer
	Cat         *categoriser.Service
	Provider    ai.Provider
	DisplayCC   string
	FX          fx.Source // daily rates for foreign-currency rows; nil = manual rates only
	AfterCommit func(batchID string)
	Log         *slog.Logger
}

type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

var ErrDuplicateFile = errors.New("this exact file was already imported")

type Batch struct {
	*core.ImportBatch
	Rows       int        `json:"rows"`
	SchemaName string     `json:"schema_name,omitempty"`
	Sheets     []string   `json:"sheets,omitempty"`
	Detection  *Detection `json:"detection,omitempty"`
	ParseError *string    `json:"parse_error,omitempty"`
}

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
	AIProposed    bool                 `json:"ai_proposed,omitempty"`
	Amount        importer.AmountSpec  `json:"amount,omitempty"`
	FXRates       map[string]float64   `json:"fx_rates,omitempty"`
	// TargetCurrency is the account's currency; ForeignCurrencies are the
	// other currencies found in the file (they need a rate).
	TargetCurrency    string   `json:"target_currency,omitempty"`
	ForeignCurrencies []string `json:"foreign_currencies,omitempty"`
}

func (s *Service) Upload(filename string, data []byte, accountID *string) (*Batch, error) {
	if len(data) == 0 {
		return nil, &ValidationError{Message: "That file is empty."}
	}
	kind := importer.Sniff(filename, data)
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])

	var prior string
	err := s.DB.QueryRow(`SELECT b.status FROM import_batches b WHERE b.sha256 = ? AND b.status = 'confirmed'
		AND EXISTS (SELECT 1 FROM transactions t WHERE t.import_batch_id = b.id AND t.deleted_at IS NULL) LIMIT 1`, sha).Scan(&prior)
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
		_, err := tx.Exec(`INSERT INTO import_files (batch_id, content) VALUES (?,?)`, batch.ID, data)
		return err
	})
	if err != nil {
		return nil, err
	}
	b, err = s.Detect(batch.ID, kind, filename, data)
	if err != nil {
		if b == nil {
			var mb *Batch
			mb, err2 := s.markFailed(batch.ID, fmt.Sprintf("Couldn't read that file: %v", err))
			if err2 != nil {
				return nil, err
			}
			b = mb
			err = nil
		}
	}
	return b, err
}

const detectionSampleLines = 5

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
		if m, schema := s.matchSavedSchema(kind, table); schema != nil {
			return s.applySavedSchema(batchID, m, schema, data, det)
		}
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
		}
		if m, schema := s.matchSavedSchema(kind, table); schema != nil {
			return s.applySavedSchema(batchID, m, schema, data, det)
		}
		if len(table) > 0 {
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

// Adopt the schema's account before staging so duplicates are checked against
// it.
func (s *Service) applySavedSchema(batchID string, m *importer.Mapping, schema *core.ImportSchema, data []byte, det *Detection) (*Batch, error) {
	det.Columns = m.Columns
	det.Confidence = 1.0
	det.ProposedName = schema.Name
	det.MatchedSchema = schema.Name
	det.BankHint = schema.BankHint
	det.DateFormat = m.DateFormat
	det.Amount, det.FXRates = m.Amount, m.FXRates
	if schema.AccountID != nil {
		if _, err := s.DB.Exec(`UPDATE import_batches SET account_id = ? WHERE id = ?`, *schema.AccountID, batchID); err != nil {
			return nil, err
		}
	}
	return s.applyMapping(batchID, m, data, &schema.ID, det)
}

func (s *Service) AIDetect(batchID string, data []byte, filename string) (*Batch, error) {
	if s.Provider == nil {
		return nil, &ValidationError{Message: "AI provider is not configured"}
	}
	batch, err := s.getBatch(batchID)
	if err != nil {
		return nil, err
	}
	if batch.Status != "awaiting_schema" {
		return nil, &ValidationError{Message: "This import is past the mapping step"}
	}
	kind := importer.Kind(batch.Mime)
	var table [][]string
	switch kind {
	case importer.KindCSV:
		table, _, err = importer.ReadCSV(data)
	case importer.KindXLSX:
		_, table, err = importer.ReadXLSX(data, "")
	default:
		return nil, &ValidationError{Message: "AI mapping is only supported for CSV and Excel files"}
	}
	if err != nil {
		return nil, err
	}
	if len(table) < 2 {
		return nil, &ValidationError{Message: "Not enough rows to detect a schema"}
	}
	m, err := s.detectWithAI(kind, table)
	if err != nil {
		return nil, &ValidationError{Message: "AI detection failed: " + err.Error()}
	}
	det := &Detection{Kind: string(kind)}
	if batch.Detection != nil {
		_ = json.Unmarshal([]byte(*batch.Detection), det)
	}
	byName := map[string]importer.ColumnMap{}
	for _, c := range m.Columns {
		byName[c.Source] = c
	}
	det.Columns = make([]importer.ColumnMap, 0, len(table[0]))
	for _, h := range table[0] {
		h = strings.TrimSpace(h)
		c, ok := byName[h]
		if !ok {
			c = importer.ColumnMap{Source: h, Role: importer.RoleIgnore}
		}
		det.Columns = append(det.Columns, c)
	}
	det.HasHeader = m.HasHeader
	if m.Delimiter != "" {
		det.Delimiter = m.Delimiter
	}
	det.DateFormat = mappingDateFormat(m)
	det.BankHint = m.BankHint
	det.ProposedName = m.ProposedName
	det.Confidence = m.Confidence
	det.Amount = m.Amount
	det.MatchedSchema = ""
	det.Sample = sampleTable(table, detectionSampleLines+1)
	det.AIProposed = true
	s.auditSchemaDetected(batchID, filename, det)
	if err := s.markAwaitingSchema(batchID, det); err != nil {
		return nil, err
	}
	if _, err := s.DB.Exec(`UPDATE import_batches SET parse_error = NULL WHERE id = ?`, batchID); err != nil {
		return nil, err
	}
	return s.Get(batchID)
}

func mappingDateFormat(m *importer.Mapping) string {
	if m.DateFormat != "" {
		return m.DateFormat
	}
	for _, c := range m.Columns {
		if c.Role == importer.RoleDate && c.DateFormat != "" {
			return c.DateFormat
		}
	}
	return ""
}

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

func (s *Service) applyMapping(batchID string, m *importer.Mapping, data []byte, schemaID *string, det *Detection) (*Batch, error) {
	batch, err := s.getBatch(batchID)
	if err != nil {
		return nil, err
	}
	kind := importer.Kind(m.Kind)
	if kind == "" {
		kind = importer.Kind(batch.Mime)
	}
	var table [][]string
	switch kind {
	case importer.KindXLSX:
		_, table, err = importer.ReadXLSX(data, m.Sheet)
	case importer.KindCSV:
		table, _, err = importer.ReadCSV(data)
	default:
		return nil, &ValidationError{Message: "Mapping applies only to CSV and Excel files"}
	}
	if err != nil {
		return nil, err
	}
	if det != nil {
		// The header sample is the schema fingerprint, so take it from the
		// sheet actually parsed.
		det.Sample = sampleTable(table, detectionSampleLines+1)
		if err := s.storeDetection(batchID, det); err != nil {
			return nil, err
		}
	}
	target := s.targetCurrency(batch)
	rows, err := importer.ParseRows(m, table, target)
	if err != nil {
		return s.markParseError(batchID, err)
	}
	if len(rows) == 0 {
		return s.markParseError(batchID, &importer.ParseError{Message: "no rows parsed — check the column mapping"})
	}
	if det != nil {
		det.TargetCurrency, det.ForeignCurrencies = target, foreignCurrencies(rows, target)
		if err := s.storeDetection(batchID, det); err != nil {
			return nil, err
		}
	}
	if missing := s.convertCurrencies(rows, target, m.FXRates); len(missing) > 0 {
		return s.markParseError(batchID, &importer.ParseError{Message: fmt.Sprintf(
			"no exchange rate for %s → %s — enter a rate under Currency", strings.Join(missing, ", "), target)})
	}
	if _, err := s.DB.Exec(`UPDATE import_batches SET schema_id = ? WHERE id = ?`, nullStr(schemaID), batchID); err != nil {
		return nil, err
	}
	if _, err := s.stageRows(batchID, rows); err != nil {
		return nil, err
	}
	if _, err := s.DB.Exec(`UPDATE import_batches SET parse_error = NULL WHERE id = ?`, batchID); err != nil {
		return nil, err
	}
	if err := s.setStatus(batchID, "awaiting_review"); err != nil {
		return nil, err
	}
	return s.Get(batchID)
}

// targetCurrency is the batch account's currency, else the display currency.
func (s *Service) targetCurrency(batch *core.ImportBatch) string {
	if batch.AccountID != nil {
		var cc string
		if err := s.DB.QueryRow(`SELECT currency FROM accounts WHERE id = ?`, *batch.AccountID).Scan(&cc); err == nil && cc != "" {
			return strings.ToUpper(cc)
		}
	}
	if s.DisplayCC != "" {
		return s.DisplayCC
	}
	return "GBP"
}

func foreignCurrencies(rows []importer.Row, target string) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range rows {
		if r.Currency != "" && r.Currency != target && !seen[r.Currency] {
			seen[r.Currency] = true
			out = append(out, r.Currency)
		}
	}
	sort.Strings(out)
	return out
}

// convertCurrencies turns rows priced in another currency into the target
// currency, keeping what was paid as the original amount. A rate the user
// typed wins; otherwise the day's ECB rate is used. It returns the
// currencies it had no rate for (those rows are left unconverted).
func (s *Service) convertCurrencies(rows []importer.Row, target string, manual map[string]float64) []string {
	byCur := map[string][]string{}
	for _, r := range rows {
		if r.Currency != target {
			byCur[r.Currency] = append(byCur[r.Currency], r.Date)
		}
	}
	if len(byCur) == 0 {
		return nil
	}
	daily := map[string]map[string]float64{}
	for cur, dates := range byCur {
		if manual[cur] > 0 || s.FX == nil {
			continue
		}
		rates, err := s.FX.Rates(cur, target, dates)
		if err != nil {
			s.log().Warn("exchange rates unavailable", "currency", cur, "err", err)
			continue
		}
		daily[cur] = rates
	}
	missingSet := map[string]bool{}
	for i := range rows {
		r := &rows[i]
		if r.Currency == target {
			continue
		}
		rate := manual[r.Currency]
		if rate <= 0 {
			rate = daily[r.Currency][r.Date]
		}
		if rate <= 0 {
			missingSet[r.Currency] = true
			continue
		}
		r.OrigAmountMinor, r.OrigCurrency = importer.ToCurrencyMinor(r.AmountMinor, r.Currency), r.Currency
		r.AmountMinor = int64(math.Round(float64(r.AmountMinor) * rate))
		r.Currency = target
	}
	missing := make([]string, 0, len(missingSet))
	for c := range missingSet {
		missing = append(missing, c)
	}
	sort.Strings(missing)
	return missing
}

func (s *Service) stageRows(batchID string, rows []importer.Row) (int, error) {
	var count int
	err := s.DB.WithTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM import_staging WHERE batch_id = ?`, batchID); err != nil {
			return err
		}
		for _, r := range rows {
			stagingID := util.NewID()
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
			raw, _ := json.Marshal(r.Raw)
			var origAmount, origCur any
			if r.OrigCurrency != "" {
				origAmount, origCur = r.OrigAmountMinor, r.OrigCurrency
			}
			if _, err := tx.Exec(`INSERT INTO import_staging
				(id, batch_id, line_no, date, description, amount_minor, currency, source_text, destination_text, duplicate_of, raw, category_source, needs_review, created_at,
				 original_amount_minor, original_currency)
				VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				stagingID, batchID, r.Line, r.Date, r.Description, r.AmountMinor, r.Currency,
				r.SourceText, r.DestinationText, dup, string(raw), core.CategorySourceNone, 1, util.NowStr(),
				origAmount, origCur); err != nil {
				return err
			}
			count++
		}
		return nil
	})
	return count, err
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

func (s *Service) Get(batchID string) (*Batch, error) {
	batch, err := s.getBatch(batchID)
	if err != nil {
		return nil, err
	}
	b := &Batch{ImportBatch: batch, ParseError: batch.ParseError}
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
		msg = fmt.Sprintf("First problem: %s — adjust the column roles or Date format below, then re-parse.", pe.Message)
	}
	_ = s.setStatus(id, "awaiting_schema")
	if _, err := s.DB.Exec(`UPDATE import_batches SET parse_error = ? WHERE id = ?`, msg, id); err != nil {
		return nil, err
	}
	return s.Get(id)
}

func (s *Service) markAwaitingSchema(id string, det *Detection) error {
	if err := s.storeDetection(id, det); err != nil {
		return err
	}
	return s.setStatus(id, "awaiting_schema")
}

func (s *Service) storeDetection(id string, det *Detection) error {
	b, _ := json.Marshal(det)
	_, err := s.DB.Exec(`UPDATE import_batches SET detection = ? WHERE id = ?`, string(b), id)
	return err
}

func (s *Service) ApplySchemaPublic(batchID string, m *importer.Mapping, data []byte) (*Batch, error) {
	batch, err := s.getBatch(batchID)
	if err != nil {
		return nil, err
	}
	det := &Detection{}
	if batch.Detection != nil {
		_ = json.Unmarshal([]byte(*batch.Detection), det)
	}
	if m.Kind == "" {
		m.Kind = importer.Kind(batch.Mime)
	}
	det.Kind = string(m.Kind)
	det.Sheet = m.Sheet
	det.HasHeader = m.HasHeader
	det.Delimiter = m.Delimiter
	det.Columns = m.Columns
	det.DateFormat = mappingDateFormat(m)
	det.Amount, det.FXRates = m.Amount, m.FXRates
	det.MatchedSchema = ""
	det.AIProposed = false
	return s.applyMapping(batchID, m, data, nil, det)
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

func (s *Service) auditSchemaDetected(batchID, filename string, det *Detection) {
	err := s.DB.WithTx(func(tx *sql.Tx) error {
		_, err := s.Audit.Write(tx, audit.Input{
			Actor: core.ActorAI, Action: "import.schema_detected",
			EntityType: "import_batch", EntityID: batchID,
			Title: fmt.Sprintf("Detected schema %s", det.ProposedName),
			Sub:   fmt.Sprintf("%s · %.0f%% confidence · %d columns", filename, det.Confidence*100, len(det.Columns)),
			After: map[string]any{"kind": det.Kind, "columns": det.Columns, "confidence": det.Confidence},
		})
		return err
	})
	if err != nil {
		return
	}
}

func (s *Service) matchSavedSchema(kind importer.Kind, table [][]string) (*importer.Mapping, *core.ImportSchema) {
	if len(table) == 0 {
		return nil, nil
	}
	h := schemaHash(kind, table[0])
	var id, name, bank, mappingJSON string
	var accountID sql.NullString
	err := s.DB.QueryRow(`SELECT s.id, s.name, s.bank_hint, s.mapping, a.id
		FROM import_schemas s LEFT JOIN accounts a ON a.id = s.account_id AND a.deleted_at IS NULL
		WHERE s.sample_hash = ?`, h).
		Scan(&id, &name, &bank, &mappingJSON, &accountID)
	if err != nil {
		return nil, nil
	}
	var m importer.Mapping
	if err := json.Unmarshal([]byte(mappingJSON), &m); err != nil {
		return nil, nil
	}
	schema := &core.ImportSchema{ID: id, Name: name, BankHint: bank}
	if accountID.Valid {
		schema.AccountID = &accountID.String
	}
	return &m, schema
}

func schemaHash(kind importer.Kind, header []string) string {
	h := sha256.Sum256([]byte(string(kind) + "|" + strings.Join(normHeaders(header), "|")))
	return hex.EncodeToString(h[:8])
}

func normHeaders(header []string) []string {
	out := make([]string, len(header))
	for i, h := range header {
		out[i] = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, "\ufeff")))
	}
	return out
}

type StagedRow struct {
	ID                    string   `json:"id"`
	Line                  int      `json:"line_no"`
	Date                  string   `json:"date"`
	Description           string   `json:"description"`
	Destination           string   `json:"destination,omitempty"`
	AmountMinor           int64    `json:"amount_minor"`
	Currency              string   `json:"currency"`
	Type                  string   `json:"type"`
	GroupKey              string   `json:"group_key"`
	CategoryID            *string  `json:"category_id"`
	CategoryName          string   `json:"category_name"`
	CategorySource        string   `json:"category_source"`
	Confidence            *float64 `json:"category_confidence"`
	NeedsReview           bool     `json:"needs_review"`
	DuplicateOf           *string  `json:"duplicate_of,omitempty"`
	SuggestedCategoryID   *string  `json:"suggested_category_id,omitempty"`
	SuggestedCategoryName string   `json:"suggested_category_name,omitempty"`
	SuggestedConfidence   *float64 `json:"suggested_confidence,omitempty"`
	OriginalAmountMinor   *int64   `json:"original_amount_minor,omitempty"`
	OriginalCurrency      string   `json:"original_currency,omitempty"`
}

func (s *Service) Review(batchID string) (rows []*StagedRow, confident, needsReview, duplicates int, err error) {
	brows, e := s.DB.Query(`SELECT st.id, st.line_no, st.date, st.description, st.destination_text, st.amount_minor, st.currency,
		st.proposed_category_id, COALESCE(c.name,''), st.category_source, st.category_confidence, st.needs_review, st.duplicate_of,
		st.suggested_category_id, COALESCE(sc.name,''), st.suggested_confidence,
		st.original_amount_minor, COALESCE(st.original_currency, '')
		FROM import_staging st
		LEFT JOIN categories c ON c.id = st.proposed_category_id
		LEFT JOIN categories sc ON sc.id = st.suggested_category_id
		WHERE st.batch_id = ? ORDER BY st.line_no ASC`, batchID)
	if e != nil {
		return nil, 0, 0, 0, e
	}
	defer brows.Close()
	for brows.Next() {
		var r StagedRow
		var catID, dup, sugID sql.NullString
		var conf, sugConf sql.NullFloat64
		var needs int
		if e := brows.Scan(&r.ID, &r.Line, &r.Date, &r.Description, &r.Destination, &r.AmountMinor, &r.Currency,
			&catID, &r.CategoryName, &r.CategorySource, &conf, &needs, &dup,
			&sugID, &r.SuggestedCategoryName, &sugConf, &r.OriginalAmountMinor, &r.OriginalCurrency); e != nil {
			return nil, 0, 0, 0, e
		}
		r.Type = typeFor(r.AmountMinor)
		r.GroupKey = GroupKey(r.Description, r.Destination, r.AmountMinor)
		if catID.Valid {
			s := catID.String
			r.CategoryID = &s
		}
		if conf.Valid {
			f := conf.Float64
			r.Confidence = &f
		}
		if sugID.Valid {
			s := sugID.String
			r.SuggestedCategoryID = &s
		}
		if sugConf.Valid {
			f := sugConf.Float64
			r.SuggestedConfidence = &f
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

type ReviewPatch struct {
	SetCategory *struct {
		RowIDs     []string `json:"row_ids"`
		CategoryID string   `json:"category_id"`
	} `json:"set_category,omitempty"`
	AcceptSuggestions bool     `json:"accept_suggestions,omitempty"`
	AcceptConfident   bool     `json:"accept_confident"`
	ForceImport       []string `json:"force_import,omitempty"`
}

func (s *Service) ApplyReview(batchID string, p ReviewPatch) error {
	return s.DB.WithTx(func(tx *sql.Tx) error {
		if p.ForceImport != nil {
			for _, rowID := range p.ForceImport {
				if _, err := tx.Exec(`UPDATE import_staging SET duplicate_of = NULL WHERE id = ? AND batch_id = ?`,
					rowID, batchID); err != nil {
					return err
				}
			}
			if err := syncForcedRows(tx, batchID, p.ForceImport); err != nil {
				return err
			}
		}
		if p.SetCategory != nil {
			ids, err := expandToGroups(tx, batchID, p.SetCategory.RowIDs)
			if err != nil {
				return err
			}
			for _, rowID := range ids {
				var err error
				if p.SetCategory.CategoryID == "" {
					_, err = tx.Exec(`UPDATE import_staging SET proposed_category_id = NULL, category_source = 'none',
						category_confidence = NULL, needs_review = 1 WHERE id = ? AND batch_id = ?`, rowID, batchID)
				} else {
					_, err = tx.Exec(`UPDATE import_staging SET proposed_category_id = ?, category_source = 'user',
						category_confidence = NULL, needs_review = 0 WHERE id = ? AND batch_id = ?`,
						p.SetCategory.CategoryID, rowID, batchID)
				}
				if err != nil {
					return err
				}
			}
		}
		if p.AcceptSuggestions {
			if _, err := tx.Exec(`UPDATE import_staging SET proposed_category_id = suggested_category_id,
				category_source = 'ai', category_confidence = suggested_confidence, needs_review = 0
				WHERE batch_id = ? AND needs_review = 1 AND suggested_category_id IS NOT NULL`, batchID); err != nil {
				return err
			}
		}
		return nil
	})
}

type stagedKey struct {
	id, key   string
	duplicate bool
}

func loadStagedKeys(tx *sql.Tx, batchID string) ([]stagedKey, error) {
	rows, err := tx.Query(`SELECT id, description, destination_text, amount_minor, duplicate_of IS NOT NULL
		FROM import_staging WHERE batch_id = ?`, batchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []stagedKey
	for rows.Next() {
		var k stagedKey
		var desc, dest string
		var amount int64
		if err := rows.Scan(&k.id, &desc, &dest, &amount, &k.duplicate); err != nil {
			return nil, err
		}
		k.key = GroupKey(desc, dest, amount)
		out = append(out, k)
	}
	return out, rows.Err()
}

func expandToGroups(tx *sql.Tx, batchID string, rowIDs []string) ([]string, error) {
	all, err := loadStagedKeys(tx, batchID)
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, id := range rowIDs {
		want[id] = true
	}
	keys := map[string]bool{}
	for _, k := range all {
		if want[k.id] {
			keys[k.key] = true
		}
	}
	out := []string{}
	for _, k := range all {
		if want[k.id] || (keys[k.key] && !k.duplicate) {
			out = append(out, k.id)
		}
	}
	return out, nil
}

func syncForcedRows(tx *sql.Tx, batchID string, rowIDs []string) error {
	all, err := loadStagedKeys(tx, batchID)
	if err != nil {
		return err
	}
	forced := map[string]bool{}
	for _, id := range rowIDs {
		forced[id] = true
	}
	sibling := map[string]string{}
	for _, k := range all {
		if !forced[k.id] && !k.duplicate {
			sibling[k.key] = k.id
		}
	}
	for _, k := range all {
		src, ok := sibling[k.key]
		if !forced[k.id] || !ok {
			continue
		}
		if _, err := tx.Exec(`UPDATE import_staging SET
			(proposed_category_id, category_source, category_confidence, needs_review, suggested_category_id, suggested_confidence) =
			(SELECT proposed_category_id, category_source, category_confidence, needs_review, suggested_category_id, suggested_confidence
			 FROM import_staging WHERE id = ?)
			WHERE id = ?`, src, k.id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) Confirm(batchID, accountID, schemaName string) (*Batch, error) {
	batch, err := s.getBatch(batchID)
	if err != nil {
		return nil, err
	}
	if batch.Status != "awaiting_review" {
		return nil, &ValidationError{Message: "Import is not ready for review"}
	}
	err = s.DB.WithTx(func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT id, line_no, date, description, amount_minor, currency, source_text, destination_text, proposed_category_id, duplicate_of, raw,
			original_amount_minor, COALESCE(original_currency, '')
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
			origAmount                     *int64
			origCurrency                   string
		}
		var rowsData []stagedRow
		for rows.Next() {
			var r stagedRow
			var catID, dup sql.NullString
			var orig sql.NullInt64
			if err := rows.Scan(&r.id, &r.line, &r.date, &r.desc, &r.amount, &r.currency, &r.sourceText, &r.destText, &catID, &dup, &r.raw,
				&orig, &r.origCurrency); err != nil {
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
			if orig.Valid {
				n := orig.Int64
				r.origAmount = &n
			}
			rowsData = append(rowsData, r)
		}
		rows.Close()

		imported := 0
		perCategory := map[string]int{}
		var firstDate, lastDate string
		for _, r := range rowsData {
			if r.duplicateOf != nil {
				continue
			}
			t := &core.Transaction{
				AccountID: accountID, Date: r.date, Description: r.desc,
				AmountMinor: r.amount, Currency: r.currency,
				Type: s.typeForCategory(tx, r.categoryID, r.amount), CategoryID: r.categoryID,
				CategorySource: core.CategorySourceUser,
				SourceText:     r.sourceText, DestinationText: r.destText,
				ImportBatchID: &batchID, DedupeHash: repos.DedupeHash(accountID, r.date, r.amount, r.desc),
				OriginalAmountMinor: r.origAmount, OriginalCurrency: r.origCurrency,
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
	// The import is committed; a schema failure must not undo that.
	_ = s.rememberSchema(batchID, batch, accountID, schemaName)
	if s.AfterCommit != nil {
		s.AfterCommit(batchID)
	}
	return s.Get(batchID)
}

func (s *Service) rememberSchema(batchID string, batch *core.ImportBatch, accountID, name string) error {
	name = strings.TrimSpace(name)
	now := util.NowStr()
	if batch.SchemaID != nil {
		_, err := s.DB.Exec(`UPDATE import_schemas SET account_id = ?, name = COALESCE(NULLIF(?, ''), name),
			use_count = use_count + 1, last_used_at = ?, updated_at = ? WHERE id = ?`,
			accountID, name, now, now, *batch.SchemaID)
		return err
	}
	if batch.Detection == nil {
		return nil
	}
	var det Detection
	if err := json.Unmarshal([]byte(*batch.Detection), &det); err != nil {
		return err
	}
	if len(det.Sample) == 0 || len(det.Columns) == 0 {
		return nil
	}
	if name == "" {
		name = s.defaultSchemaName(&det, accountID, batch.Filename)
	}
	m := importer.Mapping{
		Kind: importer.Kind(det.Kind), Sheet: det.Sheet,
		HasHeader: det.HasHeader, Delimiter: det.Delimiter,
		Columns: det.Columns, BankHint: det.BankHint,
		DateFormat: det.DateFormat, Confidence: det.Confidence,
		Amount: det.Amount, FXRates: det.FXRates,
	}
	schema, err := s.saveSchema(name, det.BankHint, &m, det.Sample[0], det.Confidence, &accountID)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec(`UPDATE import_schemas SET use_count = use_count + 1, last_used_at = ? WHERE id = ?`, now, schema.ID)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec(`UPDATE import_batches SET schema_id = ? WHERE id = ?`, schema.ID, batchID)
	return err
}

func (s *Service) defaultSchemaName(det *Detection, accountID, filename string) string {
	if det != nil && strings.TrimSpace(det.ProposedName) != "" {
		return strings.TrimSpace(det.ProposedName)
	}
	kind := ""
	if det != nil {
		kind = strings.ToUpper(det.Kind)
	}
	if accountID != "" {
		var acct string
		if err := s.DB.QueryRow(`SELECT name FROM accounts WHERE id = ?`, accountID).Scan(&acct); err == nil && acct != "" {
			return strings.TrimSpace(acct + " " + kind)
		}
	}
	return filename
}

// enrichMerchants asks the AI for clean merchant names for a batch's first
// rows. It isn't called on import any more: it held up every import for an
// AI round trip (~10s) and covered only 20 rows. Kept for a better merchant
// naming step later.
//
//nolint:unused // kept deliberately, see above
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
		return
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

// typeForCategory lets the chosen category decide the type, so a refund put in
// a spend category is an expense and nets off that category's spend, and a row
// put in Internal transfer stays out of spend and income even before its other
// leg is imported and linked.
func (s *Service) typeForCategory(tx *sql.Tx, categoryID *string, amount int64) string {
	if categoryID != nil {
		var catType string
		_ = tx.QueryRow(`SELECT type FROM categories WHERE id = ?`, *categoryID).Scan(&catType)
		switch catType {
		case core.CategorySpend:
			return core.TypeExpense
		case core.CategoryIncome:
			return core.TypeIncome
		case core.CategoryInternal:
			return core.TypeTransfer
		}
	}
	return typeFor(amount)
}

func typeFor(amount int64) string {
	if amount >= 0 {
		return core.TypeIncome
	}
	return core.TypeExpense
}

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

func (s *Service) SaveSchema(name, bankHint string, m *importer.Mapping, sampleHeader []string, confidence float64) (*core.ImportSchema, error) {
	return s.saveSchema(name, bankHint, m, sampleHeader, confidence, nil)
}

func (s *Service) saveSchema(name, bankHint string, m *importer.Mapping, sampleHeader []string, confidence float64, accountID *string) (*core.ImportSchema, error) {
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
		AccountID: accountID,
	}
	err := s.DB.WithTx(func(tx *sql.Tx) error {
		schema.ID = util.NewID()
		schema.CreatedAt = util.NowStr()
		schema.UpdatedAt = schema.CreatedAt
		var existing string
		err := tx.QueryRow(`SELECT id FROM import_schemas WHERE sample_hash = ?`, schema.SampleHash).Scan(&existing)
		if err == nil {
			_, err = tx.Exec(`UPDATE import_schemas SET name=?, bank_hint=?, mapping=?, columns=?, confidence=?, account_id=COALESCE(?, account_id), updated_at=? WHERE id=?`,
				schema.Name, schema.BankHint, schema.Mapping, strings.Join(schema.Columns, ","), schema.Confidence, nullStr(accountID), schema.UpdatedAt, existing)
			schema.ID = existing
			return err
		}
		_, err = tx.Exec(`INSERT INTO import_schemas (id, name, bank_hint, kind, mapping, columns, sample_hash, confidence, account_id, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			schema.ID, schema.Name, schema.BankHint, schema.Kind, schema.Mapping,
			strings.Join(schema.Columns, ","), schema.SampleHash, schema.Confidence, nullStr(accountID), schema.CreatedAt, schema.UpdatedAt)
		if err != nil {
			return err
		}
		_, err = s.Audit.Write(tx, audit.Input{
			Actor: core.ActorUser, Action: "schema.saved",
			EntityType: "import_schema", EntityID: schema.ID,
			Title: fmt.Sprintf("Saved schema “%s”", schema.Name),
			Sub:   fmt.Sprintf("%d columns", len(schema.Columns)),
			After: map[string]any{"name": schema.Name, "kind": schema.Kind, "columns": schema.Columns},
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	return schema, nil
}

func (s *Service) ListSchemas() ([]*core.ImportSchema, error) {
	rows, err := s.DB.Query(`SELECT s.id, s.name, s.bank_hint, s.kind, s.mapping, s.columns, s.sample_hash, s.confidence, s.use_count, s.last_used_at, s.created_at, s.updated_at,
		a.id, COALESCE(a.name, '')
		FROM import_schemas s LEFT JOIN accounts a ON a.id = s.account_id AND a.deleted_at IS NULL
		ORDER BY s.use_count DESC, s.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*core.ImportSchema
	for rows.Next() {
		var sch core.ImportSchema
		var mapping, cols, lastUsed, accountID sql.NullString
		if err := rows.Scan(&sch.ID, &sch.Name, &sch.BankHint, &sch.Kind, &mapping, &cols, &sch.SampleHash, &sch.Confidence, &sch.UseCount, &lastUsed, &sch.CreatedAt, &sch.UpdatedAt,
			&accountID, &sch.AccountName); err != nil {
			return nil, err
		}
		if cols.Valid && cols.String != "" {
			sch.Columns = strings.Split(cols.String, ",")
		}
		if lastUsed.Valid {
			s := lastUsed.String
			sch.LastUsedAt = &s
		}
		if accountID.Valid {
			s := accountID.String
			sch.AccountID = &s
		}
		out = append(out, &sch)
	}
	return out, rows.Err()
}

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
