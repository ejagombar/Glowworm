package categoriser

import (
	"database/sql"
	"testing"

	"glowworm/internal/ai"
	"glowworm/internal/core"
	"glowworm/internal/core/audit"
	"glowworm/internal/store"
	"glowworm/internal/store/repos"
)

// capturePrompts records each categorisation prompt on its way to the mock.
type capturePrompts struct {
	ai.Mock
	prompts []map[string]any
}

func (c *capturePrompts) CompleteStructured(req *ai.StructuredRequest) (string, *ai.Usage, error) {
	if req.Capability == "categorisation" {
		c.prompts = append(c.prompts, req.PromptJSON)
	}
	return c.Mock.CompleteStructured(req)
}

func TestAIBatchSendsCategoryDescriptions(t *testing.T) {
	db, err := store.Open("sqlite", t.TempDir()+"/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	const ts = "2026-01-01T00:00:00Z"
	for _, c := range [][3]string{{"c-food", "Food & Dining", "Supermarkets and takeaways"}, {"c-fun", "Entertainment", ""}} {
		if _, err := db.Exec(`INSERT INTO categories (id, name, color, type, description, created_at, updated_at)
			VALUES (?, ?, '#0e6f51', 'spend', ?, ?, ?)`, c[0], c[1], c[2], ts, ts); err != nil {
			t.Fatal(err)
		}
	}
	cp := &capturePrompts{}
	s := &Service{DB: db, Txs: &repos.Transactions{DB: db}, Categories: &repos.Categories{DB: db},
		Settings: &repos.Settings{DB: db}, Audit: &audit.Writer{DB: db}, Provider: cp}
	txs := []*core.Transaction{{ID: "t1", Date: "2026-06-01", Description: "TESCO STORES", AmountMinor: -1000, Type: core.TypeExpense}}
	if err := db.WithTx(func(tx *sql.Tx) error { _, err := s.Categorise(tx, txs); return err }); err != nil {
		t.Fatal(err)
	}
	if len(cp.prompts) != 1 {
		t.Fatalf("got %d categorisation prompts, want 1", len(cp.prompts))
	}
	descs, _ := cp.prompts[0]["category_descriptions"].(map[string]string)
	if len(descs) != 1 || descs["Food & Dining"] != "Supermarkets and takeaways" {
		t.Fatalf("category_descriptions = %v, want only Food & Dining's", cp.prompts[0]["category_descriptions"])
	}
}
