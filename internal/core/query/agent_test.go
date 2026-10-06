package query

import (
	"encoding/json"
	"strings"
	"testing"

	"glowworm/internal/ai"
	"glowworm/internal/store"
)

func newAgentTestDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open("sqlite", t.TempDir()+"/t.db")
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func seedAgentData(t *testing.T, db *store.DB) {
	t.Helper()
	must := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	must(`INSERT INTO accounts (id, name, type, color, currency, created_at, updated_at)
		VALUES ('a1','Main','debit','#0e6f51','GBP','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z')`)
	must(`INSERT INTO categories (id, name, color, type, created_at, updated_at)
		VALUES ('c1','Transport','#0e6f51','spend','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z'),
		       ('c2','Food & Dining','#c9822e','spend','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z')`)
	for i := 0; i < 5; i++ {
		must(`INSERT INTO transactions (id, account_id, date, description, amount_minor, currency, type, category_id, dedupe_hash, created_at, updated_at)
			VALUES (?,?,?,?,?,?, 'expense','c1','h','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z')`,
			"tx"+strings.Repeat("0", 4-i)+string(rune('0'+i)), "a1", "2024-06-1"+string(rune('0'+i)), "Bus", -1000-100*i, "GBP")
	}
}

func newAgentService(t *testing.T, provider ai.Provider) *Service {
	t.Helper()
	db := newAgentTestDB(t)
	seedAgentData(t, db)
	return &Service{DB: db, Provider: provider, Clock: fixedClock{}, Repo: &ConvRepo{DB: db}}
}

type fixedClock struct{}

func (fixedClock) Today() string { return "2024-06-15" }

func TestSandboxRejectsWrites(t *testing.T) {
	db := newAgentTestDB(t)
	defer db.Close()
	bad := []string{
		"INSERT INTO transactions VALUES (1)",
		"UPDATE transactions SET amount_minor = 0",
		"DELETE FROM transactions",
		"DROP TABLE transactions",
		"CREATE TABLE x (a INT)",
		"ALTER TABLE transactions ADD COLUMN x",
		"PRAGMA journal_mode=WAL",
		"ATTACH DATABASE 'x' AS x",
		"SELECT 1; DELETE FROM transactions",
		"WITH d AS (SELECT 1) DELETE FROM transactions",
		"REPLACE INTO transactions VALUES (1)",
		"VACUUM",
	}
	for _, q := range bad {
		if _, err := RunReadonlyQuery(db, q); err == nil {
			t.Errorf("query %q should have been rejected", q)
		}
	}
}

func TestSandboxAcceptsSelects(t *testing.T) {
	db := newAgentTestDB(t)
	defer db.Close()
	good := []string{
		"SELECT 1",
		"  SELECT COUNT(*) FROM transactions  ",
		"WITH s AS (SELECT 1 AS x) SELECT * FROM s",
		"SELECT 'delete from transactions' AS lit",
		"SELECT date, SUM(amount_minor) FROM transactions GROUP BY date",
	}
	for _, q := range good {
		if _, err := RunReadonlyQuery(db, q); err != nil {
			t.Errorf("query %q should have been accepted: %v", q, err)
		}
	}
}

func TestSandboxRowCapAndTruncation(t *testing.T) {
	db := newAgentTestDB(t)
	defer db.Close()
	q := `WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x < 300) SELECT x FROM n`
	res, err := RunReadonlyQuery(db, q)
	if err != nil {
		t.Fatal(err)
	}
	if res.RowCount != MaxRows {
		t.Errorf("row cap: got %d, want %d", res.RowCount, MaxRows)
	}
	if !res.Truncated {
		t.Error("result should be marked truncated")
	}
}

func TestAgentLoopRunsQueryThenAnswers(t *testing.T) {
	provider := &ai.MockAgent{Replies: []string{
		`{"action":"run_query","sql":"SELECT c.name AS name, SUM(-t.amount_minor) AS total FROM transactions t JOIN categories c ON c.id = t.category_id WHERE t.type='expense' GROUP BY c.name ORDER BY total DESC LIMIT 10"}`,
		`{"action":"final","text":"You spent **£7,000** on Transport across 5 trips.","widgets":[{"type":"bar","title":"Spend by category","data":{"labels":["Transport"],"series":[{"name":"Spend","values":[7000]}]}}]}`,
	}}
	svc := newAgentService(t, provider)
	defer svc.DB.Close()

	var steps []string
	ans, err := svc.AskStream("how much did I spend on transport?", func(step string) { steps = append(steps, step) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ans.Text, "£7,000") {
		t.Errorf("text = %q", ans.Text)
	}
	if len(ans.Widgets) != 1 || ans.Widgets[0].Type != "bar" {
		t.Fatalf("widgets = %+v", ans.Widgets)
	}
	data := ans.Widgets[0].Data.(map[string]any)
	series := data["series"].([]map[string]any)
	if series[0]["color"] == "" {
		t.Error("expected palette color to be assigned")
	}
	joined := strings.Join(steps, "|")
	if !strings.Contains(joined, "Ran a query") || !strings.Contains(joined, "\nSELECT") {
		t.Errorf("steps missing query step: %v", steps)
	}
	if !strings.Contains(joined, "widget") {
		t.Errorf("steps missing widget step: %v", steps)
	}
}

func TestAgentLoopFeedsResultBack(t *testing.T) {
	var sawResult string
	provider := &ai.MockAgent{Replies: []string{
		`{"action":"run_query","sql":"SELECT COUNT(*) AS n FROM transactions"}`,
		`{"action":"final","text":"done","widgets":[]}`,
	}}
	svc := newAgentService(t, &captureAgent{inner: provider, capture: &sawResult})
	defer svc.DB.Close()
	if _, err := svc.Ask("count my transactions"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sawResult, "TOOL RESULT") || !strings.Contains(sawResult, "columns") {
		t.Errorf("model did not receive the tool result: %q", sawResult)
	}
}

type captureAgent struct {
	inner   ai.Provider
	capture *string
}

func (c *captureAgent) Name() string { return "capture" }
func (c *captureAgent) CompleteStructured(req *ai.StructuredRequest) (string, *ai.Usage, error) {
	if c.capture != nil {
		for _, m := range req.Messages {
			*c.capture = m.Content
		}
	}
	return c.inner.CompleteStructured(req)
}

func TestAgentFallsBackToLegacyPipeline(t *testing.T) {
	svc := newAgentService(t, &brokenAgentProvider{})
	defer svc.DB.Close()
	ans, err := svc.Ask("How much have I spent on Transport over the past year?")
	if err != nil {
		t.Fatal(err)
	}
	if ans.Plan == nil {
		t.Error("expected the legacy plan pipeline to produce the answer")
	}
	if len(ans.Widgets) != 1 {
		t.Errorf("widgets = %d, want 1", len(ans.Widgets))
	}
}

type brokenAgentProvider struct{ ai.Mock }

func (p brokenAgentProvider) CompleteStructured(req *ai.StructuredRequest) (string, *ai.Usage, error) {
	if req.Capability == "query_agent" {
		return "sorry, I cannot answer that in JSON", nil, nil
	}
	return p.Mock.CompleteStructured(req)
}

func TestSanitizeWidgets(t *testing.T) {
	specs := []agentWidgetSpec{
		{Type: "bar", Title: "ok", Data: raw(`{"labels":["a","b"],"series":[{"name":"S","values":[1,2]},{"name":"T","color":"not-a-color","values":[3,4]},{"name":"U","values":[5,6]},{"name":"V","values":[7,8]},{"name":"W","values":[9,10]},{"name":"X","values":[11,12]},{"name":"Y","values":[13,14]}]}`)},
		{Type: "pie", Title: "neg", Data: raw(`{"labels":["a","b"],"values":[-5, 10]}`)},
		{Type: "number", Title: "", Data: raw(`{"value":"£12.00"}`)},
		{Type: "table", Title: "tbl", Data: raw(`{"columns":["A","B"],"rows":[["1","2"],["3","4"]]}`)},
		{Type: "bogus", Title: "bad", Data: raw(`{}`)},
		{Type: "number", Title: "empty", Data: raw(`{"caption":"no value"}`)},
	}
	ws := sanitizeWidgets(specs)
	if len(ws) != agentMaxWidgets {
		t.Fatalf("widgets kept = %d, want %d", len(ws), agentMaxWidgets)
	}
	bar := ws[0].Data.(map[string]any)
	series := bar["series"].([]map[string]any)
	if len(series) != 6 {
		t.Errorf("series cap: got %d, want 6", len(series))
	}
	if series[0]["color"] != "#0e6f51" {
		t.Errorf("palette not applied: %v", series[0]["color"])
	}
	if series[1]["color"] != "#4f5bc7" {
		t.Errorf("invalid color not replaced: %v", series[1]["color"])
	}
	pie := ws[1].Data.(map[string]any)
	if pie["values"].([]int64)[0] != 0 {
		t.Error("negative pie value should clamp to 0")
	}
	if ws[2].Type != "number" {
		t.Errorf("third widget should be the number, got %q", ws[2].Type)
	}
}

func TestSanitizeWidgetsCap(t *testing.T) {
	specs := make([]agentWidgetSpec, 6)
	for i := range specs {
		specs[i] = agentWidgetSpec{Type: "number", Title: "n", Data: raw(`{"value":"1"}`)}
	}
	if got := len(sanitizeWidgets(specs)); got != agentMaxWidgets {
		t.Errorf("widget cap: got %d, want %d", got, agentMaxWidgets)
	}
}

func raw(s string) json.RawMessage { return json.RawMessage(s) }

func TestExampleQueriesRun(t *testing.T) {
	db := newAgentTestDB(t)
	defer db.Close()
	must := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	must(`INSERT INTO accounts (id, name, type, color, currency, opening_balance_minor, created_at, updated_at)
		VALUES ('a1','Main','debit','#0e6f51','GBP',10000,'2024-01-01T00:00:00Z','2024-01-01T00:00:00Z')`)
	must(`INSERT INTO transactions (id, account_id, date, description, amount_minor, currency, type, dedupe_hash, created_at, updated_at)
		VALUES ('t1','a1','2024-01-05','in',5000,'GBP','income','h','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z'),
		       ('t2','a1','2024-02-05','out',-2000,'GBP','expense','h','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z'),
		       ('t3','a1','2024-03-05','out',-1000,'GBP','expense','h','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z')`)

	res, err := RunReadonlyQuery(db, `SELECT a.name, a.opening_balance_minor + COALESCE((SELECT SUM(t.amount_minor)
		FROM transactions t WHERE t.account_id = a.id AND t.deleted_at IS NULL
		AND (a.opening_balance_date IS NULL OR a.opening_balance_date = '' OR t.date > a.opening_balance_date)), 0)
		AS balance FROM accounts a WHERE a.deleted_at IS NULL`)
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Rows[0][1].(int64); got != 12000 {
		t.Errorf("balance example: got %v, want 12000", got)
	}

	res, err = RunReadonlyQuery(db, `WITH months AS (SELECT DISTINCT strftime('%Y-%m', date) AS m FROM transactions WHERE deleted_at IS NULL)
		SELECT m.m AS month, (SELECT COALESCE(SUM(opening_balance_minor),0) FROM accounts WHERE deleted_at IS NULL)
		+ COALESCE(SUM(t.amount_minor), 0) AS balance
		FROM months m LEFT JOIN transactions t ON t.deleted_at IS NULL AND strftime('%Y-%m', t.date) <= m.m
		GROUP BY m.m ORDER BY m.m`)
	if err != nil {
		t.Fatal(err)
	}
	want := []int64{15000, 13000, 12000}
	for i, w := range want {
		if got := res.Rows[i][1].(int64); got != w {
			t.Errorf("series[%d] = %v, want %v", i, got, w)
		}
	}

	res, err = RunReadonlyQuery(db, `SELECT c.name AS category, SUM(-t.amount_minor) AS spend FROM transactions t
		LEFT JOIN categories c ON c.id = t.category_id
		WHERE t.deleted_at IS NULL AND t.type = 'expense' AND t.date BETWEEN '2024-01-01' AND '2024-06-30'
		GROUP BY 1 ORDER BY spend DESC LIMIT 10`)
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Rows[0][1].(int64); got != 3000 {
		t.Errorf("spend example: got %v, want 3000", got)
	}

	if got := stripFences("```sql\nSELECT 1\n```"); got != "SELECT 1" {
		t.Errorf("stripFences: %q", got)
	}
}

func TestDayEntriesAndSummaries(t *testing.T) {
	svc := newAgentService(t, &ai.Mock{})
	defer svc.DB.Close()
	db := svc.DB
	must := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	must(`INSERT INTO conversations (id, title, created_at, updated_at) VALUES ('cv1','C1','2024-06-15T10:00:00Z','2024-06-15T10:00:00Z')`)
	must(`INSERT INTO conversations (id, title, created_at, updated_at) VALUES ('cv2','C2','2024-06-14T10:00:00Z','2024-06-14T10:00:00Z')`)
	must(`INSERT INTO messages (id, conversation_id, role, text, steps, created_at)
		VALUES ('m1','cv1','user','How much did I spend on Transport?','[]','2024-06-15T10:00:00Z'),
		       ('m2','cv1','assistant','You spent £35.00.','[]','2024-06-15T10:00:05Z'),
		       ('m3','cv2','user','What is my balance?','[]','2024-06-14T10:00:00Z')`)

	entries, err := svc.DayEntries()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	today, yest := entries[0], entries[1]
	if today.Date != "2024-06-15" || today.Count != 2 || today.ConversationID != "cv1" {
		t.Errorf("today entry = %+v", today)
	}
	if yest.Date != "2024-06-14" || yest.Count != 1 {
		t.Errorf("yesterday entry = %+v", yest)
	}
	if today.HasSummary {
		t.Error("no summary should exist yet")
	}

	sum, err := svc.SummariseDay("2024-06-15")
	if err != nil {
		t.Fatal(err)
	}
	if sum == "" || len(sum) > 50 {
		t.Errorf("summary = %q", sum)
	}
	entries, _ = svc.DayEntries()
	if !entries[0].HasSummary || entries[0].Summary != sum {
		t.Errorf("summary not joined: %+v", entries[0])
	}

	svc.CatchUpSummaries()
	entries, _ = svc.DayEntries()
	if !entries[1].HasSummary {
		t.Error("catch-up did not fill yesterday's summary")
	}

	svc2 := newAgentService(t, nil)
	defer svc2.DB.Close()
	svc2.CatchUpSummaries()
}

// The merchant column is often empty on imported rows; matching on merchant
// alone made "Virgin Trains" return no expenses.
func TestMerchantDescriptionFallback(t *testing.T) {
	db := newAgentTestDB(t)
	defer db.Close()
	must := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	must(`INSERT INTO accounts (id, name, type, color, currency, created_at, updated_at)
		VALUES ('a1','Main','debit','#0e6f51','GBP','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z')`)
	must(`INSERT INTO transactions (id, account_id, date, description, amount_minor, currency, type, dedupe_hash, created_at, updated_at)
		VALUES ('t1','a1','2024-06-22','Virgin Trains',-1275,'GBP','expense','h','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z'),
		       ('t2','a1','2024-06-30','Virgin Trains',-1275,'GBP','expense','h','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z')`)

	res, err := RunReadonlyQuery(db, `SELECT SUM(-t.amount_minor) AS spend, COUNT(*) AS n, MIN(t.date) AS first, MAX(t.date) AS last
		FROM transactions t
		WHERE t.deleted_at IS NULL AND t.type = 'expense'
		AND COALESCE(NULLIF(t.merchant,''), t.description) LIKE '%Virgin Trains%'`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Rows[0][0].(int64) != 2550 || res.Rows[0][1].(int64) != 2 {
		t.Errorf("merchant fallback: got %v", res.Rows[0])
	}
	res2, err := RunReadonlyQuery(db, `SELECT COUNT(*) FROM transactions WHERE merchant = 'Virgin Trains'`)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Rows[0][0].(int64) != 0 {
		t.Errorf("expected merchant-only match to be empty, got %v", res2.Rows[0])
	}
}

func TestAgentPromptIncludesViewWindow(t *testing.T) {
	svc := newAgentService(t, &ai.Mock{})
	defer svc.DB.Close()
	plain := svc.agentSystemPrompt(nil)
	if strings.Contains(plain, "CURRENT VIEW PERIOD") {
		t.Error("no window → no period section")
	}
	win := &TimeWindow{From: "2025-10-01", To: "2026-10-01", Label: "Last 12 months"}
	with := svc.agentSystemPrompt(win)
	if !strings.Contains(with, "CURRENT VIEW PERIOD") ||
		!strings.Contains(with, "2025-10-01 to 2026-10-01") ||
		!strings.Contains(with, "Last 12 months") {
		t.Errorf("window not in prompt:\n%s", with)
	}
	if !strings.Contains(with, "ALWAYS a number widget") {
		t.Error("number-widget mandate missing")
	}
}

func TestListAnswerEmptyTextWithTransactionsWidget(t *testing.T) {
	provider := &ai.MockAgent{Replies: []string{
		`{"action":"run_query","sql":"SELECT * FROM transactions LIMIT 10"}`,
		`{"action":"final","text":"","widgets":[{"type":"transactions","title":"Recent transactions","data":{"transactions":[{"date":"2024-06-30","description":"Rent","category":"Bills & Utilities","amount_minor":-106266,"currency":"GBP","type":"expense"}]}}]}`,
	}}
	svc := newAgentService(t, provider)
	defer svc.DB.Close()
	ans, err := svc.Ask("list my recent transactions")
	if err != nil {
		t.Fatal(err)
	}
	if ans.Text != "" {
		t.Errorf("text should stay empty, got %q", ans.Text)
	}
	if len(ans.Widgets) != 1 || ans.Widgets[0].Type != "transactions" {
		t.Fatalf("widgets = %+v", ans.Widgets)
	}
	tx := ans.Widgets[0].Data.(map[string]any)["transactions"].([]map[string]any)[0]
	if tx["amount_minor"].(int64) != -106266 || tx["type"] != "expense" {
		t.Errorf("tx payload = %v", tx)
	}
}

func TestWidgetSpecNormalisation(t *testing.T) {
	act, err := parseAction(`{"action":"final","text":"ok","widgets":[
		{"type":"number","title":"T","value":"£5","caption":"c"},
		{"type":"transactions","title":"R","transactions":[{"date":"2024-06-30","description":"Rent","category":"Bills","amount_minor":-106266,"currency":"GBP","type":"expense"}]},
		{"type":"pie","title":"P","data":[{"label":"x","value":1}]}
	]}`)
	if err != nil {
		t.Fatal(err)
	}
	specs := act.Widgets
	if got := sanitizeWidget(specs[0]); got == nil || got.Data.(map[string]any)["value"] != "£5" {
		t.Errorf("stray keys not merged: %v", specs[0])
	}
	if got := sanitizeWidget(specs[1]); got == nil {
		t.Error("top-level transactions array not recovered")
	}
	if got := sanitizeWidget(specs[2]); got == nil {
		t.Error("array data pie not recovered")
	}
}

func TestEmptyFinalRetriesThenLegacy(t *testing.T) {
	provider := &ai.MockAgent{Replies: []string{
		`{"action":"final","text":"","widgets":[]}`,
		`{"action":"final","text":"","widgets":[{"type":"transactions","title":"Recent","data":{"transactions":[{"date":"2024-06-30","description":"Rent","category":"Bills","amount_minor":-106266,"currency":"GBP","type":"expense"}]}}]}`,
	}}
	svc := newAgentService(t, provider)
	defer svc.DB.Close()
	ans, err := svc.Ask("list my recent transactions")
	if err != nil {
		t.Fatal(err)
	}
	if len(ans.Widgets) != 1 || ans.Widgets[0].Type != "transactions" {
		t.Errorf("corrective retry failed: %+v", ans)
	}

	svc2 := newAgentService(t, &emptyThenBroken{})
	defer svc2.DB.Close()
	ans2, err := svc2.Ask("list my recent transactions")
	if err != nil {
		t.Fatal(err)
	}
	if ans2.Plan == nil {
		t.Errorf("expected legacy fallback, got %+v", ans2)
	}
}

type emptyThenBroken struct {
	rounds int
}

func (p *emptyThenBroken) Name() string { return "empty-broken" }
func (p *emptyThenBroken) CompleteStructured(req *ai.StructuredRequest) (string, *ai.Usage, error) {
	if req.Capability == "query_agent" {
		p.rounds++
		return `{"action":"final","text":"","widgets":[]}`, nil, nil
	}
	return (&ai.Mock{}).CompleteStructured(req)
}
