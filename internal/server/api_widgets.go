package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"

	"glowworm/internal/core"
	"glowworm/internal/core/audit"
	"glowworm/internal/core/query"
	"glowworm/internal/core/widgets"
	"glowworm/internal/util"
)

// query_Answer aliases the engine answer for handlers.
type query_Answer = query.Answer

// --- NL query + conversations (doc 10 §8) ---

func (a *App) apiQuery(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Text           string  `json:"text"`
		ConversationID *string `json:"conversation_id"`
	}
	if err := decodeBody(r, &in); err != nil {
		return err
	}
	ans, convID, err := a.askQuery(in.Text, in.ConversationID)
	if err != nil {
		return err
	}
	writeJSON(w, map[string]any{"conversation_id": convID, "answer": ans})
	return nil
}

// askQuery runs the pipeline and persists both messages.
func (a *App) askQuery(text string, conversationID *string) (*query.Answer, string, error) {
	var conv *core.Conversation
	var err error
	if conversationID != nil && *conversationID != "" {
		conv, err = a.Query.Repo.GetConversation(*conversationID)
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
	if _, err := a.Query.Repo.AddMessage(conv.ID, "user", text, nil, nil, nil, nil); err != nil {
		return nil, "", err
	}
	ans, err := a.Query.Ask(text)
	if err != nil {
		return nil, conv.ID, err
	}
	widgetJSON := marshalWidget(ans.Widget)
	datasetJSON := marshalAny(ans.Dataset)
	planJSON := marshalAny(ans.Plan)
	if _, err := a.Query.Repo.AddMessage(conv.ID, "assistant", ans.Text, ans.Steps, &widgetJSON, &datasetJSON, &planJSON); err != nil {
		return nil, conv.ID, err
	}
	return ans, conv.ID, nil
}

func marshalWidget(w *widgets.Widget) string {
	b, _ := json.Marshal(w)
	return string(b)
}

func marshalAny(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// apiQueryStream emits NDJSON events (doc 10 §8): steps → text → widget → done.
func (a *App) apiQueryStream(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Text           string  `json:"text"`
		ConversationID *string `json:"conversation_id"`
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
		w.Write(append(b, '\n'))
		if flusher != nil {
			flusher.Flush()
		}
	}

	send("steps", map[string]any{"items": []string{"Analysing your question"}})

	ans, convID, err := a.askQuery(in.Text, in.ConversationID)
	if err != nil {
		var ce *query.ClarifyError
		if errors.As(err, &ce) {
			send("text", map[string]any{"text": ce.Message})
			send("error", map[string]any{"message": ce.Message, "suggestions": ce.Suggestions})
			send("done", map[string]any{"conversation_id": convID})
			return nil
		}
		send("error", map[string]any{"message": err.Error()})
		send("done", map[string]any{})
		return nil
	}
	if len(ans.Steps) > 1 {
		send("steps", map[string]any{"items": ans.Steps[:len(ans.Steps)-1]})
	}
	send("text", map[string]any{"text": ans.Text})
	if ans.Widget != nil {
		send("widget", map[string]any{"widget": ans.Widget})
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

func (a *App) apiConversationGet(w http.ResponseWriter, r *http.Request) error {
	msgs, err := a.Query.Repo.Messages(r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, map[string]any{"items": msgs})
	return nil
}

func (a *App) apiConversationDelete(w http.ResponseWriter, r *http.Request) error {
	if err := a.Query.Repo.DeleteConversation(r.PathValue("id")); err != nil {
		return err
	}
	writeJSON(w, map[string]any{"ok": true})
	return nil
}

// --- pinned widgets (doc 03 §8, doc 05 §7) ---

func (a *App) apiWidgetsGet(w http.ResponseWriter, r *http.Request) error {
	list, err := a.WidgetsList()
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
		Title   string          `json:"title"`
		Type    string          `json:"type"`
		Dataset json.RawMessage `json:"dataset"`
		Plan    json.RawMessage `json:"plan"`
		Col     int             `json:"col"`
		Rows    int             `json:"rows"`
	}
	if err := decodeBody(r, &in); err != nil {
		return err
	}
	id, err := a.PinWidget(in.Title, in.Type, string(in.Dataset), string(in.Plan), in.Col, in.Rows)
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
		Title   string          `json:"title"`
		Type    string          `json:"type"`
		Pinned  bool            `json:"pinned"`
		Data    json.RawMessage `json:"data"`
		Dataset json.RawMessage `json:"dataset"`
	}
	if err := decodeBody(r, &in); err != nil {
		return err
	}
	if err := a.WidgetPatch(id, in.Title, in.Type, string(in.Dataset)); err != nil {
		return err
	}
	if !in.Pinned {
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