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

type StructuredRequest struct {
	Capability string
	PromptJSON map[string]any
	MaxTokens  int
	Timeout    time.Duration

	System   string
	Messages []ChatMessage
}

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (r *StructuredRequest) HasTranscript() bool {
	return len(r.Messages) > 0 && r.System != ""
}

type Usage struct {
	PromptTokens     int
	CompletionTokens int
	Model            string
}

type Provider interface {
	Name() string
	CompleteStructured(req *StructuredRequest) (string, *Usage, error)
}

// ErrUnavailable is a provider that couldn't be used. When it answered with an
// HTTP error, Status and Body hold what it sent so Explain can read it.
type ErrUnavailable struct {
	Reason string
	Status int
	Body   string
}

func (e *ErrUnavailable) Error() string { return "ai: " + e.Reason }

// ErrBadReply is a reply that arrived but wasn't usable JSON. Reply holds the
// raw model text for logs; Error() leaves it out because it reaches the UI.
type ErrBadReply struct {
	Reason string
	Reply  string
}

func (e *ErrBadReply) Error() string { return "ai: " + e.Reason }

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

func turnsFor(req *StructuredRequest, feedback string) (string, []ChatMessage) {
	if req.HasTranscript() {
		out := make([]ChatMessage, 0, len(req.Messages)+1)
		out = append(out, req.Messages...)
		if feedback != "" {
			out = append(out, ChatMessage{Role: "user", Content: feedback})
		}
		return req.System, out
	}
	return "", []ChatMessage{{Role: "user", Content: capabilityPrompt(req.Capability, req.PromptJSON) + feedback}}
}

func capabilityPromptInstructions(capability string) string {
	switch capability {
	case "schema_detection":
		return "Detect the statement's column mapping. Pick roles only from: date, description, amount, amount_in, amount_out, currency, balance, type_hint, memo, source, destination, original_amount, original_currency, ignore. Use original_amount/original_currency only for a second amount in the currency actually paid (e.g. \"Local amount\"). Say how money out is shown in \"amount\": {\"sign\": \"inverted\"} when a single amount column shows purchases as positive (common on credit cards), or {\"direction_column\": \"<column>\", \"out_values\": [\"DR\"]} when an always-positive amount has a separate debit/credit column; omit it for ordinary signed amounts. Provide date_format as a Go reference layout (e.g. 02/01/2006) that parses the sample dates, decimal_separator, negatives convention (signed|paren|dr_cr), has_header, delimiter for CSV, bank_hint, a short proposed_name, and a confidence between 0 and 1. Schema: {kind: csv|xlsx|ofx|qif|pdf, has_header: bool, delimiter: string, columns: [{source, role, date_format, decimal_separator}], amount: {sign, direction_column, out_values}, bank_hint, proposed_name, confidence}."
	case "categorisation":
		return `Categorise bank-statement transactions for one person. For each one, first work out who was paid and what for, as the person reading their own statement would, then pick the best of allowed_categories (use the names exactly).
Working out the payee:
- Bank descriptions are terse. Look past payment-processor prefixes and suffixes (SQ *, SumUp, Zettle, iZ *, DOJO*, PAYPAL *, CRV*, Google/Apple Pay), store and terminal numbers, references, phone numbers and town names to the business itself.
- Use what you know: chains, brands, apps, transport operators, utilities. For a business you don't know, judge from the name what kind of place it is (pub, café, bakery, restaurant, takeaway, shop, gym, rail or bus operator) — most names say enough.
- Foreign-language names, foreign towns or company suffixes from abroad suggest spending while travelling.
- A payment to or from a person takes its meaning from the reference after the name ("To Sam - rent", "From Alex - wifi").
- The amount and count help: a few pounds at a café is a coffee, tens of pounds at a supermarket is a shop, a fixed monthly amount is a bill or subscription.
past_choices, when given, are how this person categorised other transactions: follow their habits for the same kind of payee (if they put pubs under one category, a new pub goes there too). category_descriptions, when given, are their notes on what a category holds and win over your reading of its name.
Use a catch-all category (General, Other, Miscellaneous and the like) only when you truly can't tell what was bought.
"merchant" is the business or person you identified, cleaned up. "confidence" is your honest probability (0-1) that the category is right: 0.85 or more for a business you recognise or a name that plainly says what kind of place it is; below 0.6 only when you can't tell what the payee is.
Schema: {items: [{index, merchant, category, confidence}]}. Answer every index.`
	case "query_agent":
		return `You are in an agent loop with tools. Reply with ONE JSON object: either {"action":"run_query","sql":"SELECT ..."} to run a read-only query, or {"action":"final","text":"...","widgets":[...]} to finish. Never respond with prose outside JSON.`
	case "merchant_extraction":
		return "Extract a clean merchant name for each description. Schema: {items: [{index, merchant, confidence}]}."
	case "chat_summary":
		return "Summarise what the user asked about and what was answered in one day of chat. Schema: {summary: string}. The summary must be at most 50 characters, impersonal (no first person), no quotes and no trailing punctuation. Example: \"Transport spend, balance trends\"."
	case "schema_naming":
		return "Propose a short schema name like \"Monzo CSV\". Schema: {name: string}."
	default:
		return "Respond with JSON only."
	}
}

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
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<22))
	if err != nil {
		// OpenRouter pads slow non-streaming replies with whitespace keep-alives,
		// so a deadline mid-body leaves only blanks: report it as the timeout it is.
		if ctx.Err() != nil {
			return &ErrUnavailable{Reason: "provider timed out before replying"}
		}
		return &ErrUnavailable{Reason: "reading provider response: " + err.Error()}
	}
	if resp.StatusCode/100 != 2 {
		return httpError(resp, data)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return &ErrBadReply{Reason: "unreadable provider response: " + err.Error(), Reply: truncate(string(data), 2000)}
	}
	return nil
}

func httpError(resp *http.Response, body []byte) *ErrUnavailable {
	return &ErrUnavailable{
		Reason: fmt.Sprintf("%s: %s", resp.Status, truncate(string(body), 300)),
		Status: resp.StatusCode,
		Body:   truncate(string(body), 4000),
	}
}

func extractJSON(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimPrefix(s, "```")
		if i := strings.LastIndex(s, "```"); i >= 0 {
			s = s[:i]
		}
		s = strings.TrimSpace(s)
	}
	start := strings.IndexAny(s, "{[")
	end := strings.LastIndexAny(s, "}]")
	if start < 0 || end <= start {
		return "", &ErrBadReply{Reason: "no JSON in reply", Reply: truncate(raw, 2000)}
	}
	return s[start : end+1], nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func retry(fn func(feedback string) (string, *Usage, error), validate func(string) error) (string, *Usage, error) {
	var feedback, lastOut string
	var lastErr error
	var usage *Usage
	for attempt := 0; attempt < 3; attempt++ {
		out, u, err := fn(feedback)
		if err != nil {
			return "", u, err
		}
		usage = u
		if err := validate(out); err != nil {
			lastErr, lastOut = err, out
			feedback = fmt.Sprintf("Your previous reply was invalid: %v. Reply with JSON only.", err)
			continue
		}
		return out, usage, nil
	}
	return "", usage, &ErrBadReply{Reason: fmt.Sprintf("reply was not valid JSON after 3 attempts (%v)", lastErr), Reply: truncate(lastOut, 2000)}
}
