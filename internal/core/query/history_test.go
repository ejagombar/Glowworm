package query

import (
	"strings"
	"testing"
	"time"

	"glowworm/internal/ai"
	"glowworm/internal/core"
)

var histNow = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

func histMsg(role, text string, ago time.Duration, steps ...string) *core.Message {
	return &core.Message{Role: role, Text: text, Steps: steps, CreatedAt: histNow.Add(-ago).Format(time.RFC3339)}
}

func questions(turns []Turn) []string {
	var out []string
	for _, t := range turns {
		out = append(out, t.Question)
	}
	return out
}

func TestRecentTurnsWithinTheGap(t *testing.T) {
	msgs := []*core.Message{
		histMsg("user", "yesterday's question", 20*time.Hour),
		histMsg("assistant", "old answer", 20*time.Hour),
		histMsg("user", "q1", 70*time.Minute),
		histMsg("assistant", "a1", 69*time.Minute),
		histMsg("user", "q2", 45*time.Minute),
		histMsg("assistant", "a2", 44*time.Minute),
		histMsg("user", "failed question", 30*time.Minute), // its answer failed: never saved
		histMsg("user", "q3", 20*time.Minute),
		histMsg("assistant", "a3", 19*time.Minute),
		histMsg("user", "q4", 5*time.Minute),
		histMsg("assistant", "a4", 4*time.Minute),
	}
	// Each question came within 30 minutes of the next, back to q2; q1 was
	// 24 minutes before q2, so it would count too, but only 3 are sent.
	if got := strings.Join(questions(RecentTurns(msgs, histNow)), ","); got != "q2,q3,q4" {
		t.Errorf("turns = %s, want q2,q3,q4", got)
	}
	// After a break of more than 30 minutes, nothing goes.
	if got := RecentTurns(msgs, histNow.Add(40*time.Minute)); len(got) != 0 {
		t.Errorf("after a break: %v", questions(got))
	}
	// A gap in the middle cuts off everything before it.
	gap := []*core.Message{
		histMsg("user", "before", 3*time.Hour), histMsg("assistant", "x", 3*time.Hour),
		histMsg("user", "after", 10*time.Minute), histMsg("assistant", "y", 9*time.Minute),
	}
	if got := strings.Join(questions(RecentTurns(gap, histNow)), ","); got != "after" {
		t.Errorf("turns = %s, want after", got)
	}
}

func TestTurnKeepsSQLThatRanAndWidgetQueries(t *testing.T) {
	widget := `[{"type":"number","title":"Total spend","data":{"value":"£120.00"},"plan":{"v":2,"sql":"SELECT SUM(-amount_minor) FROM transactions"}}]`
	a := histMsg("assistant", "£120.00 on Amex.", time.Minute,
		"Query failed: no such column\nSELECT nope FROM transactions",
		"Ran a query → 3 rows\nSELECT name FROM accounts",
		"Built 1 widget for the answer")
	a.Widget = &widget
	turn := RecentTurns([]*core.Message{histMsg("user", "amex spend", 2*time.Minute), a}, histNow)[0]
	if len(turn.SQL) != 1 || turn.SQL[0] != "SELECT name FROM accounts" {
		t.Errorf("SQL = %q", turn.SQL)
	}
	block := historyBlock([]Turn{turn})
	for _, want := range []string{"Q: amex spend", "A: £120.00 on Amex.", "SQL: SELECT name FROM accounts",
		`Widget (number) "Total spend" = £120.00: SELECT SUM(-amount_minor) FROM transactions`} {
		if !strings.Contains(block, want) {
			t.Errorf("block lacks %q:\n%s", want, block)
		}
	}
	if strings.Contains(block, "nope") {
		t.Errorf("block has the failed SQL:\n%s", block)
	}
}

func TestHistoryBlockDropsOldestWhenLong(t *testing.T) {
	// Each item is cut to its own limit, but a turn with several queries
	// and widgets is still about 3,600 characters.
	long := strings.Repeat("x", 2000)
	big := func(q string) Turn {
		w := TurnWidget{Type: "bar", Title: "t", SQL: long}
		return Turn{Question: q, Answer: long, SQL: []string{long, long, long}, Widgets: []TurnWidget{w, w, w}}
	}
	turns := []Turn{big("oldest"), big("middle"), {Question: "newest", Answer: "short"}}
	block := historyBlock(turns)
	if len(block) > historyMaxChars {
		t.Errorf("block is %d chars", len(block))
	}
	if strings.Contains(block, "Q: oldest") || !strings.Contains(block, "Q: middle") || !strings.Contains(block, "Q: newest") {
		t.Errorf("wrong turns kept:\n%s", block)
	}
	if historyBlock(nil) != "" {
		t.Error("no turns should give no block")
	}
}

// recordingAgent keeps the requests the agent sends.
type recordingAgent struct {
	ai.MockAgent
	reqs []*ai.StructuredRequest
}

func (p *recordingAgent) CompleteStructured(req *ai.StructuredRequest) (string, *ai.Usage, error) {
	p.reqs = append(p.reqs, req)
	return p.MockAgent.CompleteStructured(req)
}

func TestAgentSendsHistoryWithTheQuestion(t *testing.T) {
	provider := &recordingAgent{MockAgent: ai.MockAgent{Replies: []string{
		`{"action":"final","text":"£40.00 on Amex.","widgets":[]}`,
		`{"action":"final","text":"£50.00 on transport.","widgets":[]}`,
	}}}
	svc := newAgentService(t, provider)
	defer func() { _ = svc.DB.Close() }()

	var steps []string
	history := []Turn{{Question: "amex spend this month", Answer: "£120.00.", SQL: []string{"SELECT 1"}}}
	if _, err := svc.AskStreamWindow("and last month?", history, nil, func(s string) { steps = append(steps, s) }); err != nil {
		t.Fatal(err)
	}
	first := provider.reqs[0].Messages[0].Content
	for _, want := range []string{"EARLIER IN THIS CHAT", "Q: amex spend this month", "SQL: SELECT 1", "USING EARLIER TURNS", "QUESTION: and last month?"} {
		if !strings.Contains(first, want) {
			t.Errorf("first message lacks %q:\n%s", want, first)
		}
	}
	if strings.Contains(provider.reqs[0].System, "EARLIER IN THIS CHAT") {
		t.Error("history went into the system prompt, which should stay the same between questions")
	}
	if len(steps) == 0 || steps[0] != "Used 1 earlier question as context" {
		t.Errorf("steps = %q", steps)
	}

	// Without history the question goes as it is.
	if _, err := svc.AskStreamWindow("transport spend", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := provider.reqs[1].Messages[0].Content; got != "transport spend" {
		t.Errorf("first message = %q", got)
	}
}
