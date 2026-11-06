package imports

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"glowworm/internal/ai"
	"glowworm/internal/core"
	"glowworm/internal/core/audit"
	"glowworm/internal/core/auditexec"
	"glowworm/internal/core/categoriser"
	"glowworm/internal/importer"
	"glowworm/internal/store"
	"glowworm/internal/store/repos"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	db, err := store.Open("sqlite", t.TempDir()+"/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	w := &audit.Writer{DB: db}
	txs := &repos.Transactions{DB: db}
	cat := &categoriser.Service{DB: db, Txs: txs, Categories: &repos.Categories{DB: db},
		Settings: &repos.Settings{DB: db}, Audit: w, Provider: &ai.Mock{}}
	return &Service{DB: db, Accounts: &repos.Accounts{DB: db}, Txs: txs, Audit: w,
		Cat: cat, Provider: &ai.Mock{}, DisplayCC: "GBP"}
}

func mustExec(t *testing.T, s *Service, q string, args ...any) {
	t.Helper()
	if _, err := s.DB.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

func seedImportFixtures(t *testing.T, s *Service) {
	t.Helper()
	const ts = "2026-01-01T00:00:00Z"
	for _, id := range []string{"acc-main", "acc-joint"} {
		mustExec(t, s, `INSERT INTO accounts (id, name, type, color, currency, created_at, updated_at)
			VALUES (?, ?, 'debit', '#0e6f51', 'GBP', ?, ?)`, id, map[string]string{"acc-main": "Main", "acc-joint": "Joint"}[id], ts, ts)
	}
	for id, name := range map[string]string{"c-food": "Food & Dining", "c-fun": "Entertainment", "c-transport": "Transport"} {
		mustExec(t, s, `INSERT INTO categories (id, name, color, type, created_at, updated_at)
			VALUES (?, ?, '#0e6f51', 'spend', ?, ?)`, id, name, ts, ts)
	}
	mustExec(t, s, `INSERT INTO category_rules (pattern, category_id, votes, updated_at) VALUES ('netflix', 'c-fun', 3, ?)`, ts)
}

const statementCSV = `Date,Description,Payee,Amount
2026-06-01,TESCO STORES 1234,,-10.00
2026-06-02,TESCO STORES 5678,,-12.50
2026-06-03,Coffee Shop,Bean Co,-3.50
2026-06-04,Coffee Shop,Bean Co,-3.50
2026-06-05,Coffee Shop,Other Cafe,-4.00
2026-06-06,Netflix,,-9.99
2026-06-07,Salary,,2000.00
`

func userMapping() *importer.Mapping {
	return &importer.Mapping{
		Kind: importer.KindCSV, HasHeader: true, Delimiter: ",", Confidence: 0.9,
		Columns: []importer.ColumnMap{
			{Source: "Date", Role: importer.RoleDate},
			{Source: "Description", Role: importer.RoleDesc},
			{Source: "Payee", Role: importer.RoleDestination},
			{Source: "Amount", Role: importer.RoleAmount},
		},
	}
}

type stagedState struct {
	category, source, suggested string
	needsReview                 bool
}

func stagedByDesc(t *testing.T, s *Service, batchID string) map[string][]stagedState {
	t.Helper()
	rows, err := s.DB.Query(`SELECT description || '|' || destination_text, COALESCE(proposed_category_id,''), category_source,
		COALESCE(suggested_category_id,''), needs_review FROM import_staging WHERE batch_id = ? ORDER BY line_no`, batchID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string][]stagedState{}
	for rows.Next() {
		var key string
		var st stagedState
		var needs int
		if err := rows.Scan(&key, &st.category, &st.source, &st.suggested, &needs); err != nil {
			t.Fatal(err)
		}
		st.needsReview = needs == 1
		out[key] = append(out[key], st)
	}
	return out
}

func TestAIDetectIsOnlyAProposal(t *testing.T) {
	s := newTestService(t)
	seedImportFixtures(t, s)
	acc := "acc-main"
	b, err := s.Upload("statement.csv", []byte(statementCSV), &acc)
	if err != nil {
		t.Fatal(err)
	}
	if b.Status != "awaiting_schema" {
		t.Fatalf("status after upload = %q, want awaiting_schema", b.Status)
	}
	b, err = s.AIDetect(b.ID, []byte(statementCSV), "statement.csv")
	if err != nil {
		t.Fatal(err)
	}
	if b.Status != "awaiting_schema" || b.Rows != 0 {
		t.Fatalf("AI detection must wait for review: status=%q rows=%d", b.Status, b.Rows)
	}
	if b.Detection == nil || !b.Detection.AIProposed || b.Detection.ProposedName != "Mock CSV" {
		t.Fatalf("detection should carry the AI proposal for review: %+v", b.Detection)
	}
	if cols := b.Detection.Columns; len(cols) != 4 || cols[2].Source != "Payee" || cols[2].Role != importer.RoleIgnore {
		t.Fatalf("every file column should be offered for review: %+v", cols)
	}
	b, err = s.ApplySchemaPublic(b.ID, userMapping(), []byte(statementCSV))
	if err != nil {
		t.Fatal(err)
	}
	if b.Status != "awaiting_review" || b.Rows != 7 {
		t.Fatalf("after accept: status=%q rows=%d", b.Status, b.Rows)
	}
	if b.Detection.AIProposed || len(b.Detection.Columns) != 4 {
		t.Fatalf("detection should now describe the applied mapping: %+v", b.Detection)
	}
}

func TestCategoriseAIManyChunks(t *testing.T) {
	s := newTestService(t)
	seedImportFixtures(t, s)
	words := strings.Fields("alpha bravo charlie delta echo foxtrot golf hotel india juliet kilo lima mike november oscar papa quebec romeo sierra tango uniform victor whiskey")
	var csv strings.Builder
	csv.WriteString("Date,Description,Payee,Amount\n")
	for i, w := range words {
		fmt.Fprintf(&csv, "2026-06-%02d,TESCO %s,,-1.00\n", i%28+1, w)
	}
	acc := "acc-main"
	b, err := s.Upload("many.csv", []byte(csv.String()), &acc)
	if err != nil {
		t.Fatal(err)
	}
	if b, err = s.ApplySchemaPublic(b.ID, userMapping(), []byte(csv.String())); err != nil {
		t.Fatal(err)
	}
	var events []Progress
	if err := s.CategoriseAI(context.Background(), b.ID, func(p Progress) { events = append(events, p) }); err != nil {
		t.Fatal(err)
	}
	groups, progress := 0, 0
	for _, ev := range events {
		switch ev.Phase {
		case "group":
			groups++
		case "ai_progress":
			progress++
		case "warning":
			t.Fatalf("unexpected warning: %s", ev.Message)
		}
	}
	wantChunks := (len(words) + aiChunkSize - 1) / aiChunkSize
	if groups != len(words) || progress != wantChunks {
		t.Fatalf("got %d group events and %d progress events, want %d and %d", groups, progress, len(words), wantChunks)
	}
	for desc, rs := range stagedByDesc(t, s, b.ID) {
		if rs[0].category != "c-food" || rs[0].source != core.CategorySourceAI {
			t.Fatalf("%s not placed by the AI: %+v", desc, rs[0])
		}
	}
}

// capturePrompts records each categorisation prompt on its way to the mock.
type capturePrompts struct {
	ai.Mock
	mu      sync.Mutex
	prompts []map[string]any
}

func (c *capturePrompts) CompleteStructured(req *ai.StructuredRequest) (string, *ai.Usage, error) {
	if req.Capability == "categorisation" {
		c.mu.Lock()
		c.prompts = append(c.prompts, req.PromptJSON)
		c.mu.Unlock()
	}
	return c.Mock.CompleteStructured(req)
}

func TestCategoriseAISendsCategoryDescriptions(t *testing.T) {
	s := newTestService(t)
	seedImportFixtures(t, s)
	mustExec(t, s, `UPDATE categories SET description = 'Cinema, gigs and streaming' WHERE id = 'c-fun'`)
	cp := &capturePrompts{}
	s.Cat.Provider = cp
	acc := "acc-main"
	b, err := s.Upload("statement.csv", []byte(statementCSV), &acc)
	if err != nil {
		t.Fatal(err)
	}
	if b, err = s.ApplySchemaPublic(b.ID, userMapping(), []byte(statementCSV)); err != nil {
		t.Fatal(err)
	}
	if err := s.CategoriseAI(context.Background(), b.ID, func(Progress) {}); err != nil {
		t.Fatal(err)
	}
	if len(cp.prompts) == 0 {
		t.Fatal("the AI was never asked")
	}
	for _, p := range cp.prompts {
		descs, ok := p["category_descriptions"].(map[string]string)
		if !ok {
			t.Fatalf("prompt has no category_descriptions: %v", p)
		}
		if len(descs) != 1 || descs["Entertainment"] != "Cinema, gigs and streaming" {
			t.Fatalf("category_descriptions = %v, want only Entertainment's", descs)
		}
	}
}

func TestCategoriseGroupsReviewAndRememberSchema(t *testing.T) {
	s := newTestService(t)
	seedImportFixtures(t, s)
	acc := "acc-main"
	b, err := s.Upload("statement.csv", []byte(statementCSV), &acc)
	if err != nil {
		t.Fatal(err)
	}
	if b, err = s.ApplySchemaPublic(b.ID, userMapping(), []byte(statementCSV)); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	var events []Progress
	emit := func(p Progress) { events = append(events, p) }
	if err := s.CategoriseLocal(ctx, b.ID, emit); err != nil {
		t.Fatal(err)
	}
	st := stagedByDesc(t, s, b.ID)
	if got := st["Netflix|"][0]; got.category != "c-fun" || got.source != core.CategorySourceLocal {
		t.Fatalf("rule should place Netflix locally: %+v", got)
	}
	var otherCafe string
	_ = s.DB.QueryRow(`SELECT id FROM import_staging WHERE batch_id = ? AND destination_text = 'Other Cafe'`, b.ID).Scan(&otherCafe)
	if err := s.ApplyReview(b.ID, ReviewPatch{SetCategory: &struct {
		RowIDs     []string `json:"row_ids"`
		CategoryID string   `json:"category_id"`
	}{RowIDs: []string{otherCafe}, CategoryID: "c-food"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.CategoriseAI(ctx, b.ID, emit); err != nil {
		t.Fatal(err)
	}

	st = stagedByDesc(t, s, b.ID)
	for _, r := range st["TESCO STORES 1234|"] {
		if r.category != "c-food" || r.source != core.CategorySourceAI {
			t.Fatalf("confident AI answer should be applied: %+v", r)
		}
	}
	if r := st["TESCO STORES 5678|"][0]; r.category != "c-food" {
		t.Fatalf("same normalised description must share the group's category: %+v", r)
	}
	for _, r := range st["Coffee Shop|Bean Co"] {
		if r.category != "" || !r.needsReview || r.suggested == "" {
			t.Fatalf("low-confidence AI answer must stay a suggestion: %+v", r)
		}
	}
	if r := st["Coffee Shop|Other Cafe"][0]; r.category != "c-food" || r.source != core.CategorySourceUser {
		t.Fatalf("user decision overwritten by the AI pass: %+v", r)
	}
	if r := st["Salary|"][0]; r.suggested != "c-system-income" || !r.needsReview {
		t.Fatalf("income row should get an income suggestion: %+v", r)
	}

	var tesco string
	_ = s.DB.QueryRow(`SELECT id FROM import_staging WHERE batch_id = ? AND description = 'TESCO STORES 5678'`, b.ID).Scan(&tesco)
	if err := s.ApplyReview(b.ID, ReviewPatch{SetCategory: &struct {
		RowIDs     []string `json:"row_ids"`
		CategoryID string   `json:"category_id"`
	}{RowIDs: []string{tesco}, CategoryID: "c-transport"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyReview(b.ID, ReviewPatch{AcceptSuggestions: true}); err != nil {
		t.Fatal(err)
	}
	st = stagedByDesc(t, s, b.ID)
	for _, key := range []string{"TESCO STORES 1234|", "TESCO STORES 5678|"} {
		if r := st[key][0]; r.category != "c-transport" {
			t.Fatalf("group split on edit: %s → %+v", key, r)
		}
	}
	for key, rows := range st {
		for _, r := range rows {
			if r.needsReview {
				t.Fatalf("%s still needs review after accepting suggestions", key)
			}
		}
	}

	b, err = s.Confirm(b.ID, "acc-main", "My Bank CSV")
	if err != nil {
		t.Fatal(err)
	}
	if b.SchemaName != "My Bank CSV" {
		t.Fatalf("schema not linked to batch: %q", b.SchemaName)
	}
	schemas, err := s.ListSchemas()
	if err != nil {
		t.Fatal(err)
	}
	if len(schemas) != 1 || schemas[0].AccountName != "Main" || schemas[0].UseCount != 1 {
		t.Fatalf("saved schema = %+v", schemas)
	}
	var mapping string
	_ = s.DB.QueryRow(`SELECT mapping FROM import_schemas`).Scan(&mapping)
	if !strings.Contains(mapping, `"destination"`) {
		t.Fatalf("saved schema must be the mapping the user applied: %s", mapping)
	}

	next := strings.ReplaceAll(statementCSV, "2026-06-0", "2026-07-0")
	joint := "acc-joint"
	b2, err := s.Upload("july.csv", []byte(next), &joint)
	if err != nil {
		t.Fatal(err)
	}
	if b2.Status != "awaiting_review" || b2.SchemaName != "My Bank CSV" {
		t.Fatalf("saved schema not matched: status=%q schema=%q", b2.Status, b2.SchemaName)
	}
	if b2.AccountID == nil || *b2.AccountID != "acc-main" {
		t.Fatalf("matched schema should preselect its account, got %v", b2.AccountID)
	}

	// The same export saved with a byte-order mark (as Excel does) is the
	// same schema, not a new one.
	withBOM := "\ufeff" + strings.ReplaceAll(statementCSV, "2026-06-0", "2026-08-0")
	b3, err := s.Upload("august.csv", []byte(withBOM), &joint)
	if err != nil {
		t.Fatal(err)
	}
	if b3.Status != "awaiting_review" || b3.SchemaName != "My Bank CSV" {
		t.Fatalf("schema not matched with a byte-order mark: status=%q schema=%q", b3.Status, b3.SchemaName)
	}
}

func TestForcedDuplicateJoinsItsGroup(t *testing.T) {
	s := newTestService(t)
	seedImportFixtures(t, s)
	acc := "acc-main"
	b, err := s.Upload("june.csv", []byte(statementCSV), &acc)
	if err != nil {
		t.Fatal(err)
	}
	if b, err = s.ApplySchemaPublic(b.ID, userMapping(), []byte(statementCSV)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Confirm(b.ID, acc, ""); err != nil {
		t.Fatal(err)
	}
	again := statementCSV + "2026-06-10,TESCO STORES 4321,,-7.00\n"
	b2, err := s.Upload("june-again.csv", []byte(again), &acc)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Categorise(context.Background(), b2.ID, func(Progress) {}); err != nil {
		t.Fatal(err)
	}
	var dupTesco string
	_ = s.DB.QueryRow(`SELECT id FROM import_staging WHERE batch_id = ? AND description = 'TESCO STORES 1234' AND duplicate_of IS NOT NULL`, b2.ID).Scan(&dupTesco)
	if dupTesco == "" {
		t.Fatal("expected the old Tesco row to be flagged as a duplicate")
	}
	if err := s.ApplyReview(b2.ID, ReviewPatch{ForceImport: []string{dupTesco}}); err != nil {
		t.Fatal(err)
	}
	st := stagedByDesc(t, s, b2.ID)
	if forced, fresh := st["TESCO STORES 1234|"][0], st["TESCO STORES 4321|"][0]; forced.category != fresh.category || forced.category != "c-food" {
		t.Fatalf("forced row should take its group's decision: forced=%+v group=%+v", forced, fresh)
	}
}

func TestGroupKey(t *testing.T) {
	cases := []struct {
		a, b [2]string
		same bool
	}{
		{[2]string{"TESCO STORES 1234", ""}, [2]string{"Tesco Stores 9876", ""}, true},
		{[2]string{"Coffee Shop", "Bean Co"}, [2]string{"Coffee Shop", "bean  co"}, true},
		{[2]string{"Coffee Shop", "Bean Co"}, [2]string{"Coffee Shop", "Other Cafe"}, false},
		{[2]string{"Transfer", "12345678"}, [2]string{"Transfer", "87654321"}, false},
	}
	for _, c := range cases {
		got := GroupKey(c.a[0], c.a[1], -100) == GroupKey(c.b[0], c.b[1], -100)
		if got != c.same {
			t.Errorf("GroupKey(%q) vs (%q): same=%v, want %v", c.a, c.b, got, c.same)
		}
	}
	if GroupKey("Refund", "", 100) == GroupKey("Refund", "", -100) {
		t.Error("income and expense rows must not share a group (categories are type-masked)")
	}
}

type fakeFX map[string]float64 // currency → rate for every date

func (f fakeFX) Rates(from, to string, dates []string) (map[string]float64, error) {
	out := map[string]float64{}
	if r, ok := f[from]; ok {
		for _, d := range dates {
			out[d] = r
		}
	}
	return out, nil
}

func TestForeignCurrencyRowsAreConverted(t *testing.T) {
	s := newTestService(t)
	seedImportFixtures(t, s)
	csv := "Date,Description,Amount,Currency\n2026-06-01,Cafe Paris,-10.00,EUR\n2026-06-02,Tesco,-5.00,GBP\n2026-06-03,Kiosk,-1000,JPY\n"
	m := &importer.Mapping{Kind: "csv", HasHeader: true, Columns: []importer.ColumnMap{
		{Source: "Date", Role: importer.RoleDate}, {Source: "Description", Role: importer.RoleDesc},
		{Source: "Amount", Role: importer.RoleAmount}, {Source: "Currency", Role: importer.RoleCurrency},
	}}
	acc := "acc-main"
	upload := func() *Batch {
		b, err := s.Upload(fmt.Sprintf("trip-%d.csv", len(t.Name())), []byte(csv), &acc)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}

	s.FX = fakeFX{"EUR": 0.85}
	b := upload()
	got, err := s.ApplySchemaPublic(b.ID, m, []byte(csv))
	if err != nil {
		t.Fatal(err)
	}
	if got.Status == "awaiting_review" || got.ParseError == nil || !strings.Contains(*got.ParseError, "JPY") {
		t.Fatalf("a currency with no rate must stop at the mapping step: status=%s err=%v", got.Status, got.ParseError)
	}
	if d := got.Detection; d == nil || strings.Join(d.ForeignCurrencies, ",") != "EUR,JPY" || d.TargetCurrency != "GBP" {
		t.Fatalf("detection should list the foreign currencies: %+v", got.Detection)
	}

	m.FXRates = map[string]float64{"JPY": 0.0052} // typed by the user
	got, err = s.ApplySchemaPublic(b.ID, m, []byte(csv))
	if err != nil || got.Status != "awaiting_review" {
		t.Fatalf("status=%s err=%v parse=%v", got.Status, err, got.ParseError)
	}
	rows, _, _, _, err := s.Review(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	byDesc := map[string]*StagedRow{}
	for _, r := range rows {
		byDesc[r.Description] = r
	}
	if r := byDesc["Cafe Paris"]; r.AmountMinor != -850 || r.Currency != "GBP" || r.OriginalCurrency != "EUR" || *r.OriginalAmountMinor != -1000 {
		t.Fatalf("EUR row = %+v", r)
	}
	if r := byDesc["Kiosk"]; r.AmountMinor != -520 || r.OriginalCurrency != "JPY" || *r.OriginalAmountMinor != -1000 {
		t.Fatalf("JPY row with a typed rate = %+v", r)
	}
	if r := byDesc["Tesco"]; r.OriginalCurrency != "" || r.AmountMinor != -500 {
		t.Fatalf("home-currency row = %+v", r)
	}
}

// HTML-escaped text ("Marks &amp; Spencer") is decoded on import, and a row
// imported before that, saved escaped with the escaped text's hash, is still
// recognised as a duplicate.
func TestImportDecodesHTMLEntities(t *testing.T) {
	s := newTestService(t)
	seedImportFixtures(t, s)
	csv := "Date,Description,Payee,Amount\n2026-06-01,Marks &amp; Spencer,B&amp;Q &lt;Ltd&gt;,-10.00\n2026-06-02,Marks &amp; Spencer,,-7.00\n"
	if _, err := s.DB.Exec(`INSERT INTO transactions (id, account_id, date, description, amount_minor, currency, type, dedupe_hash, created_at, updated_at)
		VALUES ('old','acc-main','2026-06-02','Marks & Spencer',-700,'GBP','expense',?,'2026-06-03T00:00:00Z','2026-06-03T00:00:00Z')`,
		repos.DedupeHash("acc-main", "2026-06-02", -700, "Marks &amp; Spencer")); err != nil {
		t.Fatal(err)
	}
	acc := "acc-main"
	b, err := s.Upload("chase.csv", []byte(csv), &acc)
	if err != nil {
		t.Fatal(err)
	}
	if b, err = s.ApplySchemaPublic(b.ID, userMapping(), []byte(csv)); err != nil {
		t.Fatal(err)
	}
	rows, err := s.DB.Query(`SELECT description, destination_text, COALESCE(duplicate_of, '') FROM import_staging WHERE batch_id = ? ORDER BY line_no`, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var got []string
	for rows.Next() {
		var d, dest, dup string
		if err := rows.Scan(&d, &dest, &dup); err != nil {
			t.Fatal(err)
		}
		got = append(got, d+"|"+dest+"|"+dup)
	}
	want := []string{"Marks & Spencer|B&Q <Ltd>|", "Marks & Spencer||old"}
	if strings.Join(got, ";") != strings.Join(want, ";") {
		t.Fatalf("staged = %v, want %v", got, want)
	}
}

// A PDF is refused up front: no batch is created and it never reaches the
// CSV parser's column mapping.
func TestPDFIsRefused(t *testing.T) {
	s := newTestService(t)
	seedImportFixtures(t, s)
	acc := "acc-main"
	_, err := s.Upload("statement.pdf", []byte("%PDF-1.7\n1 0 obj\n"), &acc)
	var ve *ValidationError
	if !errors.As(err, &ve) || !strings.Contains(ve.Message, "PDF") {
		t.Fatalf("err = %v, want a validation error about PDFs", err)
	}
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM import_batches`).Scan(&n); err != nil || n != 0 {
		t.Errorf("batches = %d (%v), want none", n, err)
	}
}

const statementQIF = "!Type:Bank\nD01/06/2026\nT-10.00\nPTesco Stores\n^\nD02/06/2026\nT2000.00\nPSalary\nMJune\n^\n"

// A QIF file stops at the column mapping like a CSV, rather than going
// straight to review.
func TestQIFGoesThroughMapping(t *testing.T) {
	s := newTestService(t)
	seedImportFixtures(t, s)
	acc := "acc-main"
	b, err := s.Upload("statement.qif", []byte(statementQIF), &acc)
	if err != nil {
		t.Fatal(err)
	}
	if b.Status != "awaiting_schema" || b.Detection == nil {
		t.Fatalf("status = %s, detection = %v", b.Status, b.Detection)
	}
	if got := strings.Join(b.Detection.Sample[0], ","); got != "Date,Payee,Memo,Amount" {
		t.Fatalf("sample header = %s", got)
	}
	d := b.Detection
	m := &importer.Mapping{Kind: importer.KindQIF, HasHeader: true, Columns: d.Columns, DateFormat: d.DateFormat}
	if b, err = s.ApplySchemaPublic(b.ID, m, []byte(statementQIF)); err != nil {
		t.Fatal(err)
	}
	if b.Status != "awaiting_review" || b.Rows != 2 {
		t.Fatalf("after mapping: status %s, %d rows, %v", b.Status, b.Rows, b.ParseError)
	}
	st := stagedByDesc(t, s, b.ID)
	if len(st["Salary — June|"]) != 1 {
		t.Fatalf("staged = %v", st)
	}
	if _, err := s.Confirm(b.ID, acc, "My QIF"); err != nil {
		t.Fatal(err)
	}

	// No schema is remembered, so the next QIF stops at the mapping too.
	next := strings.ReplaceAll(statementQIF, "/06/2026", "/07/2026")
	b, err = s.Upload("next.qif", []byte(next), &acc)
	if err != nil {
		t.Fatal(err)
	}
	if b.Status != "awaiting_schema" {
		t.Fatalf("second QIF: status = %s", b.Status)
	}
}

// An OFX file stops at the mapping too, and a re-import of it is caught
// as duplicates.
func TestOFXGoesThroughMappingAndDedupes(t *testing.T) {
	s := newTestService(t)
	seedImportFixtures(t, s)
	acc := "acc-main"
	ofx := "<OFX><STMTTRN><DTPOSTED>20260601120000[0:GMT]<TRNAMT>-10.00<NAME>Tesco<FITID>A1</STMTTRN>" +
		"<STMTTRN><DTPOSTED>20260602<TRNAMT>5.00<PAYEE>Refund<MEMO>June<FITID>A2</STMTTRN></OFX>"
	upload := func(name string) *Batch {
		ofx += "\n" // not byte-identical, so the per-row check is what catches it
		b, err := s.Upload(name, []byte(ofx), &acc)
		if err != nil {
			t.Fatal(err)
		}
		if b.Status != "awaiting_schema" {
			t.Fatalf("%s: status = %s", name, b.Status)
		}
		m := &importer.Mapping{Kind: importer.KindOFX, HasHeader: true, Columns: b.Detection.Columns}
		if b, err = s.ApplySchemaPublic(b.ID, m, []byte(ofx)); err != nil {
			t.Fatal(err)
		}
		if b.Status != "awaiting_review" || b.Rows != 2 {
			t.Fatalf("%s after mapping: %s, %d rows, %v", name, b.Status, b.Rows, b.ParseError)
		}
		return b
	}
	b := upload("a.ofx")
	if len(stagedByDesc(t, s, b.ID)["Refund — June|"]) != 1 {
		t.Fatalf("staged = %v", stagedByDesc(t, s, b.ID))
	}
	if _, err := s.Confirm(b.ID, acc, ""); err != nil {
		t.Fatal(err)
	}
	b = upload("b.ofx")
	var dups int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM import_staging WHERE batch_id = ? AND duplicate_of IS NOT NULL`, b.ID).Scan(&dups)
	if dups != 2 {
		t.Fatalf("re-imported OFX: %d duplicates, want 2", dups)
	}
}

// Moving a finished import changes its transactions' account; undo puts back
// only the ones still where the move left them.
func TestMoveImportToAccountAndUndo(t *testing.T) {
	s := newTestService(t)
	seedImportFixtures(t, s)
	acc := "acc-main"
	b, err := s.Upload("statement.csv", []byte(statementCSV), &acc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApplySchemaPublic(b.ID, userMapping(), []byte(statementCSV)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Confirm(b.ID, acc, ""); err != nil {
		t.Fatal(err)
	}
	count := func(account string) int {
		var n int
		_ = s.DB.QueryRow(`SELECT COUNT(*) FROM transactions WHERE import_batch_id = ? AND account_id = ? AND deleted_at IS NULL`, b.ID, account).Scan(&n)
		return n
	}
	if _, err := s.MoveToAccount(b.ID, "acc-joint"); err != nil {
		t.Fatal(err)
	}
	if count("acc-joint") != 7 || count("acc-main") != 0 {
		t.Fatalf("after move: joint %d, main %d", count("acc-joint"), count("acc-main"))
	}
	var hash string
	_ = s.DB.QueryRow(`SELECT dedupe_hash FROM transactions WHERE import_batch_id = ? AND description = 'Netflix'`, b.ID).Scan(&hash)
	if hash != repos.DedupeHash("acc-joint", "2026-06-06", -999, "Netflix") {
		t.Errorf("dedupe hash not moved with the account")
	}
	var moveID int64
	_ = s.DB.QueryRow(`SELECT MAX(id) FROM audit_entries WHERE action = 'import.edited'`).Scan(&moveID)

	// One transaction is moved back by hand; undoing the import move leaves it.
	mustExec(t, s, `UPDATE transactions SET account_id = 'acc-main' WHERE import_batch_id = ? AND description = 'Salary'`, b.ID)
	engine := audit.NewEngine(s.DB, s.Audit)
	x := &auditexec.Executor{DB: s.DB, Txs: s.Txs, Accounts: s.Accounts, Categories: &repos.Categories{DB: s.DB}}
	notes, err := engine.Undo(moveID, x.Apply)
	if err != nil {
		t.Fatal(err)
	}
	if count("acc-main") != 7 || len(notes) != 1 {
		t.Fatalf("after undo: main %d, notes %q", count("acc-main"), notes)
	}
	if _, err := engine.Redo([]int64{moveID}, x.Apply); err != nil {
		t.Fatal(err)
	}
	if count("acc-joint") != 7 {
		t.Fatalf("after redo: joint %d", count("acc-joint"))
	}

	// An account with no currency set uses the display currency.
	mustExec(t, s, `UPDATE accounts SET currency = '' WHERE id = 'acc-main'`)
	if _, err := s.MoveToAccount(b.ID, "acc-main"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MoveToAccount(b.ID, "nope"); err == nil {
		t.Error("moving to a missing account should fail")
	}
}

// flakyAI fails the first categorisation request, then leaves the last item
// out of every reply and lower-cases the category names.
type flakyAI struct {
	ai.Mock
	mu    sync.Mutex
	calls int
}

func (f *flakyAI) CompleteStructured(req *ai.StructuredRequest) (string, *ai.Usage, error) {
	if req.Capability != "categorisation" {
		return f.Mock.CompleteStructured(req)
	}
	f.mu.Lock()
	f.calls++
	first := f.calls == 1
	f.mu.Unlock()
	if first {
		return "", nil, &ai.ErrBadReply{Reason: "no JSON in reply"}
	}
	resp, usage, err := f.Mock.CompleteStructured(req)
	if err != nil {
		return resp, usage, err
	}
	var parsed struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal([]byte(resp), &parsed); err != nil || len(parsed.Items) == 0 {
		return resp, usage, err
	}
	part := parsed.Items
	if len(part) > 1 {
		part = part[:len(part)-1]
	}
	for _, it := range part {
		it["category"] = strings.ToLower(fmt.Sprint(it["category"]))
	}
	b, _ := json.Marshal(map[string]any{"items": part})
	return string(b), usage, nil
}

// A chunk that fails or answers in part is asked again for the rest, and a
// category named in the wrong case still counts; one failure doesn't stop
// the other chunks.
func TestCategoriseAIRetriesChunks(t *testing.T) {
	s := newTestService(t)
	seedImportFixtures(t, s)
	s.Cat.Provider = &flakyAI{}
	var csv strings.Builder
	csv.WriteString("Date,Description,Payee,Amount\n")
	for i, w := range strings.Fields("alpha bravo charlie delta echo foxtrot golf") {
		fmt.Fprintf(&csv, "2026-06-%02d,TESCO %s,,-1.00\n", i+1, w)
	}
	acc := "acc-main"
	b, err := s.Upload("flaky.csv", []byte(csv.String()), &acc)
	if err != nil {
		t.Fatal(err)
	}
	if b, err = s.ApplySchemaPublic(b.ID, userMapping(), []byte(csv.String())); err != nil {
		t.Fatal(err)
	}
	var placed int
	if err := s.CategoriseAI(context.Background(), b.ID, func(p Progress) {
		if p.Phase == "warning" {
			t.Errorf("unexpected warning: %s", p.Message)
		}
	}); err != nil {
		t.Fatal(err)
	}
	for desc, rs := range stagedByDesc(t, s, b.ID) {
		if rs[0].category == "c-food" {
			placed++
		} else {
			t.Errorf("%s not placed: %+v", desc, rs[0])
		}
	}
	if placed != 7 {
		t.Errorf("placed %d of 7", placed)
	}
}

// Changing the account under review checks for duplicates in the new one.
func TestSetReviewAccountRechecksDuplicates(t *testing.T) {
	s := newTestService(t)
	seedImportFixtures(t, s)
	mustExec(t, s, `INSERT INTO accounts (id, name, type, color, currency, created_at, updated_at)
		VALUES ('acc-other','Other','debit','#000','GBP','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`)
	acc := "acc-main"
	b, err := s.Upload("statement.csv", []byte(statementCSV), &acc)
	if err != nil {
		t.Fatal(err)
	}
	if b, err = s.ApplySchemaPublic(b.ID, userMapping(), []byte(statementCSV)); err != nil {
		t.Fatal(err)
	}
	dups := func() int {
		var n int
		_ = s.DB.QueryRow(`SELECT COUNT(*) FROM import_staging WHERE batch_id = ? AND duplicate_of IS NOT NULL`, b.ID).Scan(&n)
		return n
	}
	before := dups()
	// One of the rows is already in the other account.
	var date, desc string
	var amount int64
	_ = s.DB.QueryRow(`SELECT date, description, amount_minor FROM import_staging WHERE batch_id = ? AND duplicate_of IS NULL LIMIT 1`, b.ID).
		Scan(&date, &desc, &amount)
	mustExec(t, s, `INSERT INTO transactions (id, account_id, date, description, amount_minor, currency, type, dedupe_hash, created_at, updated_at)
		VALUES ('t-there','acc-other',?,?,?,'GBP','expense',?,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
		date, desc, amount, repos.DedupeHash("acc-other", date, amount, desc))
	if err := s.SetReviewAccount(b.ID, "acc-other"); err != nil {
		t.Fatal(err)
	}
	if got := dups(); got != 1 {
		t.Errorf("duplicates in the other account = %d, want 1 (were %d in the first)", got, before)
	}
	if got, _ := s.Get(b.ID); got == nil || got.AccountID == nil || *got.AccountID != "acc-other" {
		t.Errorf("batch account = %+v", got)
	}
	if err := s.SetReviewAccount(b.ID, "nope"); err == nil {
		t.Error("an unknown account was accepted")
	}
}
