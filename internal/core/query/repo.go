// Conversation/message persistence (doc 02 §8.2).
package query

import (
	"database/sql"
	"encoding/json"
	"strings"

	"glowworm/internal/core"
	"glowworm/internal/store"
	"glowworm/internal/util"
)

// ConvRepo implements ConversationRepo over SQL.
type ConvRepo struct{ DB *store.DB }

func (r *ConvRepo) CreateConversation(title string) (*core.Conversation, error) {
	c := &core.Conversation{ID: util.NewID(), Title: title, CreatedAt: util.NowStr(), UpdatedAt: c0()}
	c.UpdatedAt = c.CreatedAt
	_, err := r.DB.Exec(`INSERT INTO conversations (id, title, created_at, updated_at) VALUES (?,?,?,?)`,
		c.ID, c.Title, c.CreatedAt, c.UpdatedAt)
	return c, err
}

func c0() string { return util.NowStr() }

func (r *ConvRepo) AddMessage(conversationID, role, text string, steps []string, widget, dataset, plan *string) (*core.Message, error) {
	stepsJSON := "[]"
	if len(steps) > 0 {
		b, _ := jsonMarshal(steps)
		stepsJSON = string(b)
	}
	m := &core.Message{ID: util.NewID(), ConversationID: conversationID, Role: role,
		Text: text, Steps: steps, Widget: widget, Dataset: dataset, Plan: plan,
		CreatedAt: util.NowStr()}
	_, err := r.DB.Exec(`INSERT INTO messages (id, conversation_id, role, text, steps, widget, dataset, plan, created_at)
		VALUES (?,?,?,?,?,?,?,?,?)`,
		m.ID, conversationID, role, text, stepsJSON, derefP(widget), derefP(dataset), derefP(plan), m.CreatedAt)
	if err != nil {
		return nil, err
	}
	_, err = r.DB.Exec(`UPDATE conversations SET updated_at = ? WHERE id = ?`, m.CreatedAt, conversationID)
	return m, err
}

func derefP(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

// MessagesPage returns up to limit messages ending just before the cursor
// "created_at|id" — the newest page when before is nil. Results are ascending
// (oldest first). The row-value tiebreak keeps pages stable when messages
// share a created_at second (a user question and its assistant reply do).
func (r *ConvRepo) MessagesPage(conversationID string, before *string, limit int) ([]*core.Message, error) {
	if limit <= 0 {
		limit = 20
	}
	q := `SELECT id, conversation_id, role, text, steps, widget, dataset, plan, created_at
		FROM messages WHERE conversation_id = ?`
	args := []any{conversationID}
	if before != nil && *before != "" {
		if parts := strings.SplitN(*before, "|", 2); len(parts) == 2 {
			q += ` AND (created_at, id) < (?, ?)`
			args = append(args, parts[0], parts[1])
		} else {
			q += ` AND created_at < ?`
			args = append(args, *before)
		}
	}
	q += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := r.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*core.Message
	for rows.Next() {
		m, scanErr := scanMessage(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Reverse DESC → ascending (oldest first) for rendering.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// scanMessage scans one message row (shared by Messages and MessagesPage).
func scanMessage(rows *sql.Rows) (*core.Message, error) {
	var m core.Message
	var steps, widget, dataset, plan sql.NullString
	if err := rows.Scan(&m.ID, &m.ConversationID, &m.Role, &m.Text, &steps, &widget, &dataset, &plan, &m.CreatedAt); err != nil {
		return nil, err
	}
	if steps.Valid && steps.String != "" {
		_ = json.Unmarshal([]byte(steps.String), &m.Steps)
	}
	if widget.Valid && widget.String != "" {
		w := widget.String
		m.Widget = &w
	}
	if dataset.Valid && dataset.String != "" {
		d := dataset.String
		m.Dataset = &d
	}
	if plan.Valid && plan.String != "" {
		p := plan.String
		m.Plan = &p
	}
	return &m, nil
}

func (r *ConvRepo) ListConversations() ([]*core.Conversation, error) {
	rows, err := r.DB.Query(`SELECT id, title, created_at, updated_at FROM conversations ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*core.Conversation
	for rows.Next() {
		var c core.Conversation
		if err := rows.Scan(&c.ID, &c.Title, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, &c)
	}
	return out, rows.Err()
}

func (r *ConvRepo) GetConversation(id string) (*core.Conversation, error) {
	var c core.Conversation
	err := r.DB.QueryRow(`SELECT id, title, created_at, updated_at FROM conversations WHERE id = ?`, id).
		Scan(&c.ID, &c.Title, &c.CreatedAt, &c.UpdatedAt)
	return &c, err
}

func (r *ConvRepo) Messages(conversationID string) ([]*core.Message, error) {
	rows, err := r.DB.Query(`SELECT id, conversation_id, role, text, steps, widget, dataset, plan, created_at
		FROM messages WHERE conversation_id = ? ORDER BY created_at ASC, id ASC`, conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*core.Message
	for rows.Next() {
		var m core.Message
		var steps, widget, dataset, plan sql.NullString
		if err := rows.Scan(&m.ID, &m.ConversationID, &m.Role, &m.Text, &steps, &widget, &dataset, &plan, &m.CreatedAt); err != nil {
			return nil, err
		}
		// steps is stored as a JSON array; scan as a string and decode.
		if steps.Valid && steps.String != "" {
			var s []string
			if err := json.Unmarshal([]byte(steps.String), &s); err == nil {
				m.Steps = s
			}
		}
		if widget.Valid {
			s := widget.String
			m.Widget = &s
		}
		if dataset.Valid {
			s := dataset.String
			m.Dataset = &s
		}
		if plan.Valid {
			s := plan.String
			m.Plan = &s
		}
		out = append(out, &m)
	}
	return out, rows.Err()
}

func (r *ConvRepo) DeleteConversation(id string) error {
	_, err := r.DB.Exec(`DELETE FROM conversations WHERE id = ?`, id)
	return err
}
