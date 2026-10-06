package ai

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Mock is a deterministic provider for tests and the GLOWWORM_AI_MOCK=1
// dev hook (doc 07 §10: AI-fallback verified with a mock provider). It is
// never selected by user configuration.
type Mock struct{}

func (p *Mock) Name() string { return "mock" }

// MockAgent is a deterministic multi-turn agent provider for tests: it
// walks a scripted list of JSON replies (one per turn) so the query
// service's agent loop can be exercised end-to-end without a network.
type MockAgent struct {
	Replies []string // one JSON reply per agent round, in order
	turn    int
}

func (p *MockAgent) Name() string { return "mock-agent" }

func (p *MockAgent) CompleteStructured(req *StructuredRequest) (string, *Usage, error) {
	if req.Capability != "query_agent" {
		return "", nil, fmt.Errorf("mock-agent: capability %q not scripted", req.Capability)
	}
	if p.turn >= len(p.Replies) {
		return "", nil, fmt.Errorf("mock-agent: script exhausted")
	}
	r := p.Replies[p.turn]
	p.turn++
	return r, &Usage{Model: "mock-agent"}, nil
}

func (p *Mock) CompleteStructured(req *StructuredRequest) (string, *Usage, error) {
	switch req.Capability {
	case "categorisation":
		var payload struct {
			Transactions []struct {
				Index       int    `json:"index"`
				Description string `json:"description"`
			} `json:"transactions"`
			AllowedCategories map[string]string `json:"allowed_categories"`
		}
		if err := json.Unmarshal([]byte(promptPayload(req)), &payload); err != nil {
			return "", nil, err
		}
		items := []map[string]any{}
		for _, tx := range payload.Transactions {
			cat := "Uncategorised"
			d := strings.ToLower(tx.Description)
			switch {
			case strings.Contains(d, "tesco"), strings.Contains(d, "sainsbury"), strings.Contains(d, "grocer"):
				cat = pick(payload.AllowedCategories, "Food & Dining")
			case strings.Contains(d, "uber"), strings.Contains(d, "transport"), strings.Contains(d, "tfl"):
				cat = pick(payload.AllowedCategories, "Transport")
			case strings.Contains(d, "energy"), strings.Contains(d, "octopus"), strings.Contains(d, "bill"):
				cat = pick(payload.AllowedCategories, "Bills & Utilities")
			}
			items = append(items, map[string]any{"index": tx.Index, "category": cat, "confidence": 0.9})
		}
		return marshal(map[string]any{"items": items}), &Usage{Model: "mock"}, nil
	case "query_planning":
		var payload struct {
			Question   string   `json:"question"`
			Categories []string `json:"categories"`
			Accounts   []string `json:"accounts"`
		}
		if err := json.Unmarshal([]byte(promptPayload(req)), &payload); err != nil {
			return "", nil, err
		}
		q := strings.ToLower(payload.Question)
		plan := map[string]any{
			"v": 1, "metric": "spend", "breakdown": "month",
			"time":          map[string]any{"preset": "last-12m", "from": nil, "to": nil},
			"account_scope": "all", "sort": "date_asc", "limit": 12,
			"chart_hint": "bar", "title": "Spend over time",
		}
		switch {
		case strings.Contains(q, "transport"):
			plan["entity"] = map[string]any{"kind": "category", "resolved_name": "Transport", "match": "exact"}
			plan["title"] = "Transport spend over the past year"
		case strings.Contains(q, "where did my money go"):
			plan["breakdown"] = "category"
			plan["time"] = map[string]any{"preset": "last-month", "from": nil, "to": nil}
			plan["chart_hint"] = "bar"
			plan["title"] = "Where your money went this month"
		case strings.Contains(q, "balance"):
			plan["metric"] = "balance"
			plan["chart_hint"] = "line"
			plan["title"] = "Balance over the last year"
		case strings.Contains(q, "list"), strings.Contains(q, "recent"):
			plan["metric"] = "list"
			plan["breakdown"] = "none"
			plan["chart_hint"] = "transactions"
			plan["title"] = "Recent transactions"
		default:
			plan["breakdown"] = "category"
			plan["time"] = map[string]any{"preset": "last-month", "from": nil, "to": nil}
			plan["title"] = "Spending by category"
		}
		return marshal(plan), &Usage{Model: "mock"}, nil
	case "query_agent":
		// Minimal scripted agent: the last user message in the transcript is
		// either the question or a tool result. If no tool has run yet, ask for
		// total spend by category this month; when the result arrives, finish
		// with a text answer + widget built from whatever rows came back.
		last := ""
		for _, m := range req.Messages {
			last = m.Content
		}
		if strings.Contains(last, "TOOL RESULT") {
			return marshal(map[string]any{
				"action": "final",
				"text":   "You spent most on the categories in the chart below.",
				"widgets": []map[string]any{{
					"type":  "bar",
					"title": "Spending by category",
					"data": map[string]any{
						"labels": []string{"Food", "Transport"},
						"series": []map[string]any{{"name": "Spend", "values": []int64{12000, 3400}}},
					},
				}},
			}), &Usage{Model: "mock"}, nil
		}
		return marshal(map[string]any{
			"action": "run_query",
			"sql":    "SELECT c.name, SUM(-t.amount_minor) AS total FROM transactions t LEFT JOIN categories c ON c.id = t.category_id WHERE t.type = 'expense' AND t.deleted_at IS NULL GROUP BY c.name ORDER BY total DESC LIMIT 10",
		}), &Usage{Model: "mock"}, nil
	case "schema_detection":
		return marshal(map[string]any{
			"kind": "csv", "has_header": true, "delimiter": ",",
			"columns": []map[string]any{
				{"source": "Date", "role": "date"},
				{"source": "Description", "role": "description"},
				{"source": "Amount", "role": "amount"},
			},
			"bank_hint": "Mock Bank", "proposed_name": "Mock CSV", "confidence": 0.99,
		}), &Usage{Model: "mock"}, nil
	case "schema_naming":
		return marshal(map[string]any{"name": "Mock CSV"}), &Usage{Model: "mock"}, nil
	case "merchant_extraction":
		return marshal(map[string]any{"items": []any{}}), &Usage{Model: "mock"}, nil
	case "chat_summary":
		// Deterministic: lean on the day's first user question.
		var payload struct {
			Date     string `json:"date"`
			Messages []struct {
				Role string `json:"role"`
				Text string `json:"text"`
			} `json:"messages"`
		}
		_ = json.Unmarshal([]byte(promptPayload(req)), &payload)
		summary := ""
		for _, m := range payload.Messages {
			if m.Role == "user" && strings.TrimSpace(m.Text) != "" {
				summary = m.Text
				break
			}
		}
		summary = strings.Join(strings.Fields(summary), " ")
		if len(summary) > 47 {
			summary = summary[:47] + "…"
		}
		return marshal(map[string]any{"summary": summary}), &Usage{Model: "mock"}, nil
	}
	return "", nil, fmt.Errorf("mock: unknown capability %s", req.Capability)
}

func pick(m map[string]string, preferred string) string {
	for _, name := range m {
		if name == preferred {
			return preferred
		}
	}
	// fall back to the first name
	for _, name := range m {
		return name
	}
	return preferred
}

func promptPayload(req *StructuredRequest) string {
	if s, ok := req.PromptJSON["payload"].(string); ok {
		return s
	}
	b, _ := json.Marshal(req.PromptJSON)
	return string(b)
}

func marshal(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
