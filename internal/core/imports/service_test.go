package imports

import (
	"context"
	"strings"
	"testing"

	"glowworm/internal/ai"
	"glowworm/internal/core"
	"glowworm/internal/core/audit"
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
	t.Cleanup(func() { db.Close() })
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
	defer rows.Close()
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

	next := strings.Replace(statementCSV, "2026-06-0", "2026-07-0", -1)
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
