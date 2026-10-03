// Package ai implements the provider abstraction (doc 09): one internal
// interface, capability-based calls, structured JSON output with retry,
// adapters for OpenAI-compatible APIs, Anthropic, Gemini and Ollama.
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// StructuredRequest is one capability call.
type StructuredRequest struct {
	Capability string         // schema_detection|categorisation|query_planning|merchant_extraction|schema_naming
	PromptJSON map[string]any // the capability payload (embedded in the prompt)
	MaxTokens  int
	Timeout    time.Duration
}

// Usage records token counts (doc 09 §8).
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	Model            string
}

// Provider is the single internal LLM interface (doc 09 §2).
type Provider interface {
	Name() string
	// CompleteStructured returns the raw JSON string and usage.
	CompleteStructured(req *StructuredRequest) (string, *Usage, error)
}

// ErrUnavailable reports AI being configured-out or unreachable.
type ErrUnavailable struct{ Reason string }

func (e *ErrUnavailable) Error() string { return "ai: " + e.Reason }

// System preamble (doc 09 §5) shared by all capabilities.
const systemPreamble = `You are part of Glowworm, a personal finance tracker. You will be given
financial data belonging to a single user. Respond ONLY with JSON conforming
exactly to the schema provided. Never invent categories, accounts, or columns
that are not listed. Dates are ISO YYYY-MM-DD. Amounts are integers in minor
currency units (pence/cents).`

func capabilityPrompt(capability string, payload map[string]any) string {
	b, _ := json.Marshal(payload)
	return fmt.Sprintf("%s\n\nTask: %s\nPayload:\n%s\n\nRespond with JSON only.",
		systemPreamble, capabilityPromptInstructions(capability), string(b))
}

func capabilityPromptInstructions(capability string) string {
	switch capability {
	case "schema_detection":
		return "Detect the statement's column mapping. Pick roles only from: date, description, amount, amount_in, amount_out, currency, balance, type_hint, memo, source, destination, ignore. Provide date_format as a Go reference layout (e.g. 02/01/2006) that parses the sample dates, decimal_separator, negatives convention (signed|paren|dr_cr), has_header, delimiter for CSV, bank_hint, a short proposed_name, and a confidence between 0 and 1. Schema: {kind: csv|xlsx|ofx|qif|pdf, has_header: bool, delimiter: string, columns: [{source, role, date_format, decimal_separator, negatives}], bank_hint, proposed_name, confidence}."
	case "categorisation":
		return "Categorise every transaction by index using ONLY the allowed_categories names. Schema: {items: [{index, category, confidence}]}. Every index must be answered."
	case "query_planning":
		return "Translate the user's question into a query plan. Schema: {v: 1, metric: spend|income|net|balance|count|list, entity: {kind: category|account|merchant|recurring|none, id: string, resolved_name: string, match: exact|fuzzy}, breakdown: none|category|account|merchant|month|week|day, time: {preset: string|null, from: string, to: string}, account_scope: all|savings|spending|single, sort: value_desc|date_asc, limit: number, chart_hint: bar|line|pie|table|number|transactions, title: string}."
	case "merchant_extraction":
		return "Extract a clean merchant name for each description. Schema: {items: [{index, merchant, confidence}]}."
	case "schema_naming":
		return "Propose a short schema name like \"Monzo CSV\". Schema: {name: string}."
	default:
		return "Respond with JSON only."
	}
}

// --- HTTP plumbing shared by adapters ---

func postJSON(ctx context.Context, url string, headers map[string]string, body any, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return &ErrUnavailable{Reason: err.Error()}
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<22))
	if resp.StatusCode/100 != 2 {
		return &ErrUnavailable{Reason: fmt.Sprintf("%s: %s", resp.Status, truncate(string(data), 300))}
	}
	return json.Unmarshal(data, out)
}

// extractJSON tolerates code fences (doc 09 §2).
func extractJSON(s string) (string, error) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimPrefix(s, "```")
		if i := strings.LastIndex(s, "```"); i >= 0 {
			s = s[:i]
		}
		s = strings.TrimSpace(s)
	}
	// Trim to the outermost JSON object/array.
	start := strings.IndexAny(s, "{[")
	end := strings.LastIndexAny(s, "}]")
	if start < 0 || end <= start {
		return "", fmt.Errorf("ai: no JSON in reply")
	}
	return s[start : end+1], nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// retry wraps a completion with validation feedback retries (≤2, doc 09 §2).
func retry(fn func(feedback string) (string, *Usage, error), validate func(string) error) (string, *Usage, error) {
	var feedback string
	var usage *Usage
	for attempt := 0; attempt < 3; attempt++ {
		out, u, err := fn(feedback)
		if err != nil {
			return "", u, err
		}
		usage = u
		if err := validate(out); err != nil {
			feedback = fmt.Sprintf("Your previous reply was invalid: %v. Reply with JSON only.", err)
			continue
		}
		return out, usage, nil
	}
	return "", usage, fmt.Errorf("ai: structured output failed after retries")
}
