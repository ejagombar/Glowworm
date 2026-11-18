package query

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ejagombar/Glowworm/internal/core"
)

// Follow-up questions ("same but for Amex", "why was that so high?") need the
// questions just before them. The agent gets the last few answered questions
// in short form: question, answer text, the SQL that ran and the widgets'
// queries, never their result rows. Re-running SQL against the local
// database is free; sending rows costs tokens on every round. Questions only
// count as part of the same discussion while each came within
// HistoryGap of the next, so a question after a break starts afresh.
const (
	HistoryTurns     = 3
	HistoryGap       = 30 * time.Minute
	historyAnswerMax = 600
	historySQLMax    = 500
	historySQLs      = 3
	historyMaxChars  = 4800 // about 1,200 tokens
)

// Turn is an earlier question and its answer.
type Turn struct {
	Question string
	Answer   string
	SQL      []string // queries that ran without error, in order
	Widgets  []TurnWidget
}

type TurnWidget struct {
	Type, Title, SQL, Value string
}

// RecentTurns picks the answered questions to send with a new one asked at
// now, oldest first, from a conversation's latest messages in order. A
// question whose answer failed was saved without one and is left out.
func RecentTurns(msgs []*core.Message, now time.Time) []Turn {
	var turns []Turn
	last := now // the message after the one being looked at
	for i := len(msgs) - 1; i >= 0 && len(turns) < HistoryTurns; i-- {
		t, ok := msgTime(msgs[i])
		if !ok || last.Sub(t) > HistoryGap {
			break
		}
		last = t
		if msgs[i].Role == "assistant" && i > 0 && msgs[i-1].Role == "user" {
			turns = append(turns, turnFrom(msgs[i-1], msgs[i]))
		}
	}
	for i, j := 0, len(turns)-1; i < j; i, j = i+1, j-1 {
		turns[i], turns[j] = turns[j], turns[i]
	}
	return turns
}

func msgTime(m *core.Message) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339, m.CreatedAt)
	return t, err == nil
}

func turnFrom(q, a *core.Message) Turn {
	t := Turn{Question: q.Text, Answer: a.Text}
	for _, st := range a.Steps {
		if head, sql, ok := strings.Cut(st, "\n"); ok && strings.HasPrefix(head, "Ran a query") {
			t.SQL = append(t.SQL, strings.TrimSpace(sql))
		}
	}
	if a.Widget != nil {
		var ws []struct {
			Type  string `json:"type"`
			Title string `json:"title"`
			Data  struct {
				Value any `json:"value"`
			} `json:"data"`
			Plan struct {
				SQL string `json:"sql"`
			} `json:"plan"`
		}
		if json.Unmarshal([]byte(*a.Widget), &ws) == nil {
			for _, w := range ws {
				tw := TurnWidget{Type: w.Type, Title: w.Title, SQL: w.Plan.SQL}
				if w.Type == "number" && w.Data.Value != nil {
					tw.Value = fmt.Sprint(w.Data.Value)
				}
				t.Widgets = append(t.Widgets, tw)
			}
		}
	}
	return t
}

// historyBlock is the earlier turns as text for the agent, within
// historyMaxChars: the oldest turns go first when it's too long.
func historyBlock(turns []Turn) string {
	for len(turns) > 0 {
		var b strings.Builder
		b.WriteString("EARLIER IN THIS CHAT (context only, oldest first):\n")
		for i, t := range turns {
			fmt.Fprintf(&b, "\n[%d] Q: %s\n", i+1, oneLine(t.Question, 300))
			if t.Answer != "" {
				fmt.Fprintf(&b, "A: %s\n", truncateChars(strings.TrimSpace(t.Answer), historyAnswerMax))
			}
			sqls := t.SQL
			if len(sqls) > historySQLs {
				sqls = sqls[len(sqls)-historySQLs:] // the last ones are closest to the answer
			}
			for _, q := range sqls {
				fmt.Fprintf(&b, "SQL: %s\n", oneLine(q, historySQLMax))
			}
			for _, w := range t.Widgets {
				fmt.Fprintf(&b, "Widget (%s) %q", w.Type, w.Title)
				if w.Value != "" {
					fmt.Fprintf(&b, " = %s", w.Value)
				}
				if w.SQL != "" {
					fmt.Fprintf(&b, ": %s", oneLine(w.SQL, historySQLMax))
				}
				b.WriteString("\n")
			}
		}
		if b.Len() <= historyMaxChars || len(turns) == 1 {
			return truncateChars(b.String(), historyMaxChars)
		}
		turns = turns[1:]
	}
	return ""
}
