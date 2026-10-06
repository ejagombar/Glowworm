// Day entries + LLM summaries for the queries history sidebar (doc 10 §8):
// one entry per local calendar day the chat was used — date label, message
// count and a ≤50-char summary of what was talked about. Summaries are
// generated at midnight by the server scheduler, or on catch-up at next
// load if the app wasn't running.
package query

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"glowworm/internal/ai"
)

// DayEntry is one sidebar row.
type DayEntry struct {
	Date           string `json:"date"`  // YYYY-MM-DD (local)
	Label          string `json:"label"` // Today | Yesterday | 3 Oct 2026
	Count          int    `json:"count"` // messages that day
	Summary        string `json:"summary"`
	ConversationID string `json:"conversation_id"` // most recent conversation touched that day
	HasSummary     bool   `json:"-"`
}

const daySummaryMaxChars = 50

// DayEntries lists sidebar entries (newest first), joining stored summaries.
func (s *Service) DayEntries() ([]*DayEntry, error) {
	rows, err := s.DB.Query(`SELECT day, n, conversation_id FROM (
		SELECT substr(date(created_at,'localtime'),1,10) AS day, COUNT(*) AS n,
		       conversation_id, MAX(created_at) AS mx
		FROM messages GROUP BY day) ORDER BY day DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*DayEntry
	for rows.Next() {
		var e DayEntry
		if err := rows.Scan(&e.Date, &e.Count, &e.ConversationID); err != nil {
			return nil, err
		}
		out = append(out, &e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	now := time.Now()
	for _, e := range out {
		e.Label = dayEntryLabel(e.Date, now)
		var sum string
		err := s.DB.QueryRow(`SELECT summary FROM chat_day_summaries WHERE day = ?`, e.Date).Scan(&sum)
		if err == nil && strings.TrimSpace(sum) != "" {
			e.Summary, e.HasSummary = strings.TrimSpace(sum), true
		}
	}
	return out, nil
}

func dayEntryLabel(date string, now time.Time) string {
	t, err := time.ParseInLocation("2006-01-02", date, now.Location())
	if err != nil {
		return date
	}
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	switch t.Sub(today) / (24 * time.Hour) {
	case 0:
		return "Today"
	case -1:
		return "Yesterday"
	}
	return t.Format("2 Jan 2006")
}

// summaryMu serialises summary generation and de-dupes concurrent catch-ups.
var summaryMu sync.Mutex
var summarising = map[string]bool{}

// CatchUpSummaries generates summaries for every past day that has messages
// but no summary yet. Called by the midnight scheduler and (async, bounded)
// on page load — covering "app wasn't running at midnight".
func (s *Service) CatchUpSummaries() {
	summaryMu.Lock()
	if s.Provider == nil {
		summaryMu.Unlock()
		return
	}
	today := time.Now().Format("2006-01-02")
	rows, err := s.DB.Query(`SELECT day FROM (
		SELECT substr(date(created_at,'localtime'),1,10) AS day, COUNT(*) AS n
		FROM messages GROUP BY day)
		WHERE day < ? AND day NOT IN (SELECT day FROM chat_day_summaries)
		ORDER BY day DESC`, today)
	if err != nil {
		summaryMu.Unlock()
		return
	}
	var days []string
	for rows.Next() {
		var d string
		_ = rows.Scan(&d)
		days = append(days, d)
	}
	rows.Close()
	for _, d := range days {
		if summarising[d] {
			continue
		}
		summarising[d] = true
		summaryMu.Unlock()
		_, _ = s.SummariseDay(d)
		summaryMu.Lock()
		delete(summarising, d)
	}
	summaryMu.Unlock()
}

// SummariseDay generates and stores one day's summary (idempotent: it
// overwrites). Returns the summary.
func (s *Service) SummariseDay(day string) (string, error) {
	if s.Provider == nil {
		return "", nil
	}
	// The day's conversation, oldest first, mostly the user's questions.
	rows, err := s.DB.Query(`SELECT role, text FROM messages
		WHERE substr(date(created_at,'localtime'),1,10) = ?
		ORDER BY created_at ASC, id ASC LIMIT 40`, day)
	if err != nil {
		return "", err
	}
	type line struct {
		Role string `json:"role"`
		Text string `json:"text"`
	}
	var lines []line
	for rows.Next() {
		var l line
		var text string
		if err := rows.Scan(&l.Role, &text); err != nil {
			rows.Close()
			return "", err
		}
		l.Text = truncateChars(strings.TrimSpace(text), 200)
		lines = append(lines, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(lines) == 0 {
		return "", nil
	}
	started := time.Now()
	resp, usage, err := s.Provider.CompleteStructured(&ai.StructuredRequest{
		Capability: "chat_summary",
		PromptJSON: map[string]any{
			"date":     day,
			"messages": lines,
		},
		MaxTokens: 100,
		Timeout:   30 * time.Second,
	})
	s.logCall("chat_summary", started, usage, err)
	if err != nil {
		return "", err
	}
	var out struct {
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal([]byte(resp), &out); err != nil {
		return "", fmt.Errorf("summary: bad model reply: %w", err)
	}
	summary := truncateChars(strings.TrimSpace(strings.ReplaceAll(out.Summary, "\n", " ")), daySummaryMaxChars)
	if summary == "" {
		return "", fmt.Errorf("summary: empty model reply")
	}
	now := util_now()
	var count int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM messages WHERE substr(date(created_at,'localtime'),1,10) = ?`, day).Scan(&count)
	if _, err := s.DB.Exec(`INSERT INTO chat_day_summaries (day, summary, message_count, created_at, updated_at)
		VALUES (?,?,?,?,?)
		ON CONFLICT(day) DO UPDATE SET summary = excluded.summary, message_count = excluded.message_count, updated_at = excluded.updated_at`,
		day, summary, count, now, now); err != nil {
		return "", err
	}
	return summary, nil
}

func util_now() string { return time.Now().UTC().Format(time.RFC3339) }

// logCall appends to ai_calls (doc 09 §8) — best effort.
func (s *Service) logCall(capability string, started time.Time, usage *ai.Usage, err error) {
	model := ""
	pt, ct := 0, 0
	if usage != nil {
		model = usage.Model
		pt, ct = usage.PromptTokens, usage.CompletionTokens
	}
	errMsg := ""
	if err != nil {
		errMsg = err.Error()
	}
	provider := ""
	if s.Provider != nil {
		provider = s.Provider.Name()
	}
	_, _ = s.DB.Exec(`INSERT INTO ai_calls (capability, provider, model, prompt_tokens, completion_tokens, latency_ms, ok, error, created_at)
		VALUES (?,?,?,?,?,?,?,?,?)`,
		capability, provider, model, pt, ct, time.Since(started).Milliseconds(), errMsg == "", truncateChars(errMsg, 500), util_now())
}
