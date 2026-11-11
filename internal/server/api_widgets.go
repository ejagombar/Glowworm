package server

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"glowworm/internal/core"
	"glowworm/internal/core/audit"
	"glowworm/internal/core/periods"
	"glowworm/internal/core/query"
	"glowworm/internal/core/widgets"
	"glowworm/internal/util"
)

func (a *App) viewWindow(period, from, to string) *query.TimeWindow {
	if period == "" {
		period = a.Settings.Get().DefaultPeriod
		if period == "" {
			period = "all"
		}
	}
	f, t, label := periods.Resolve(period, from, to, a.Clock.TodayTime())
	if f == "" && t == "" {
		return nil
	}
	return &query.TimeWindow{From: f, To: t, Label: label}
}

func (a *App) apiQuery(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Text           string  `json:"text"`
		ConversationID *string `json:"conversation_id"`
		Period         string  `json:"period"`
		From           string  `json:"from"`
		To             string  `json:"to"`
	}
	if err := decodeBody(r, &in); err != nil {
		return err
	}
	ans, convID, err := a.askQueryWindow(in, nil)
	if err != nil {
		return err
	}
	writeJSON(w, map[string]any{"conversation_id": convID, "answer": ans})
	return nil
}

func (a *App) askQueryWindow(in struct {
	Text           string  `json:"text"`
	ConversationID *string `json:"conversation_id"`
	Period         string  `json:"period"`
	From           string  `json:"from"`
	To             string  `json:"to"`
}, onStep func(string)) (*query.Answer, string, error) {
	win := a.viewWindow(in.Period, in.From, in.To)
	text := in.Text
	var conv *core.Conversation
	var err error
	if in.ConversationID != nil && *in.ConversationID != "" {
		conv, err = a.Query.Repo.GetConversation(*in.ConversationID)
		if err != nil {
			return nil, "", err
		}
	} else {
		title := text
		if len(title) > 60 {
			title = title[:60]
		}
		conv, err = a.Query.Repo.CreateConversation(title)
		if err != nil {
			return nil, "", err
		}
	}
	// The discussion so far, read before this question is saved.
	recent, err := a.Query.Repo.MessagesPage(conv.ID, nil, 2*query.HistoryTurns+4)
	if err != nil {
		return nil, "", err
	}
	history := query.RecentTurns(recent, time.Now())
	if _, err := a.Query.Repo.AddMessage(conv.ID, "user", text, nil, nil); err != nil {
		return nil, "", err
	}
	ans, err := a.Query.Ask(text, history, win, onStep)
	if err != nil {
		return nil, conv.ID, err
	}
	if _, err := a.Query.Repo.AddMessage(conv.ID, "assistant", ans.Text, ans.Steps, marshalWidgets(ans.Widgets)); err != nil {
		return nil, conv.ID, err
	}
	return ans, conv.ID, nil
}

func marshalWidgets(ws []*widgets.Widget) *string {
	if len(ws) == 0 {
		return nil
	}
	b, _ := json.Marshal(ws)
	s := string(b)
	return &s
}

func marshalAny(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func (a *App) apiQueryStream(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Text           string  `json:"text"`
		ConversationID *string `json:"conversation_id"`
		Period         string  `json:"period"`
		From           string  `json:"from"`
		To             string  `json:"to"`
	}
	if err := decodeBody(r, &in); err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, _ := w.(http.Flusher)

	send := func(ev string, payload map[string]any) {
		m := map[string]any{"event": ev}
		for k, v := range payload {
			m[k] = v
		}
		b, _ := json.Marshal(m)
		_, _ = w.Write(append(b, '\n'))
		if flusher != nil {
			flusher.Flush()
		}
	}

	ans, convID, err := a.askQueryWindow(in, func(step string) {
		send("steps", map[string]any{"items": []string{step}})
	})
	if err != nil {
		send("error", map[string]any{"message": err.Error()})
		send("done", map[string]any{"conversation_id": convID})
		return nil
	}
	send("text", map[string]any{"text": ans.Text})
	for _, w := range ans.Widgets {
		send("widget", map[string]any{"widget": w})
	}
	send("done", map[string]any{"conversation_id": convID})
	return nil
}

func (a *App) apiConversationsList(w http.ResponseWriter, r *http.Request) error {
	convs, err := a.Query.Repo.ListConversations()
	if err != nil {
		return err
	}
	writeJSON(w, map[string]any{"items": convs})
	return nil
}

func (a *App) apiQueryDays(w http.ResponseWriter, r *http.Request) error {
	days, err := a.Query.DayEntries()
	if err != nil {
		return err
	}
	writeJSON(w, map[string]any{"items": days})
	return nil
}

func (a *App) apiConversationGet(w http.ResponseWriter, r *http.Request) error {
	msgs, err := a.Query.Repo.Messages(r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, map[string]any{"items": msgs})
	return nil
}

func (a *App) apiConversationMessages(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	before := r.URL.Query().Get("before")
	limit := 20
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 50 {
		limit = 50
	}
	var beforePtr *string
	if before != "" {
		beforePtr = &before
	}
	msgs, err := a.Query.Repo.MessagesPage(id, beforePtr, limit)
	if err != nil {
		return err
	}
	items := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		var widgets []any
		if m.Widget != nil {
			for _, w := range parseStoredWidgets(*m.Widget) {
				widgets = append(widgets, w)
			}
		}
		items = append(items, map[string]any{
			"id": m.ID, "role": m.Role, "text": m.Text, "steps": m.Steps,
			"widgets": widgets, "created_at": m.CreatedAt,
		})
	}
	writeJSON(w, map[string]any{"items": items, "has_more": len(msgs) == limit})
	return nil
}

func (a *App) apiConversationDelete(w http.ResponseWriter, r *http.Request) error {
	if err := a.Query.Repo.DeleteConversation(r.PathValue("id")); err != nil {
		return err
	}
	writeJSON(w, map[string]any{"ok": true})
	return nil
}

func (a *App) apiWidgetsGet(w http.ResponseWriter, r *http.Request) error {
	period, from, to := a.selectedPeriod(w, r)
	list, err := a.WidgetsList(period, from, to)
	if err != nil {
		return err
	}
	writeJSON(w, map[string]any{"items": list})
	return nil
}

func (a *App) apiWidgetsReorder(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Items []struct {
			ID       string `json:"id"`
			Position int    `json:"position"`
			Col      int    `json:"col"`
			Rows     int    `json:"rows"`
		} `json:"items"`
	}
	if err := decodeBody(r, &in); err != nil {
		return err
	}
	for _, item := range in.Items {
		if _, err := a.DB.Exec(`UPDATE pinned_widgets SET position=?, col=?, rows=?, updated_at=? WHERE id=?`,
			item.Position, clampSpan(item.Col, 2, 12), clampSpan(item.Rows, 1, 8), util.NowStr(), item.ID); err != nil {
			return err
		}
	}
	writeJSON(w, map[string]any{"ok": true})
	return nil
}

func clampSpan(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func (a *App) apiWidgetPin(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Title string          `json:"title"`
		Type  string          `json:"type"`
		Data  json.RawMessage `json:"data"`
		Plan  json.RawMessage `json:"plan"`
		Col   int             `json:"col"`
		Rows  int             `json:"rows"`
	}
	if err := decodeBody(r, &in); err != nil {
		return err
	}
	id, err := a.PinWidget(in.Title, in.Type, string(in.Data), string(in.Plan), in.Col, in.Rows)
	if err != nil {
		return err
	}
	if err := a.auditWidget(id, "widget.pinned", in.Title); err != nil {
		return err
	}
	writeJSON(w, map[string]any{"id": id})
	return nil
}

func (a *App) auditWidget(id, action, title string) error {
	return a.DB.WithTx(func(tx *sql.Tx) error {
		_, err := a.AuditWriter.Write(tx, audit.Input{
			Actor: "user", Action: action, EntityType: "widget", EntityID: id,
			Title: "Pinned widget", Sub: title,
		})
		return err
	})
}

func (a *App) apiWidgetPatch(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	var in struct {
		Title  string          `json:"title"`
		Type   string          `json:"type"`
		Pinned *bool           `json:"pinned"` // only an explicit false unpins
		Data   json.RawMessage `json:"data"`
	}
	if err := decodeBody(r, &in); err != nil {
		return err
	}
	if err := a.WidgetPatch(id, in.Title, in.Type, string(in.Data)); err != nil {
		return err
	}
	if in.Pinned != nil && !*in.Pinned {
		if err := a.WidgetUnpin(id); err != nil {
			return err
		}
		if err := a.auditWidget(id, "widget.unpinned", in.Title); err != nil {
			return err
		}
	} else if err := a.auditWidget(id, "widget.updated", in.Title); err != nil {
		return err
	}
	writeJSON(w, map[string]any{"ok": true})
	return nil
}

func (a *App) apiWidgetUnpin(w http.ResponseWriter, r *http.Request) error {
	if err := a.WidgetUnpin(r.PathValue("id")); err != nil {
		return err
	}
	writeJSON(w, map[string]any{"ok": true})
	return nil
}
