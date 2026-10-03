// Conversation/message persistence (doc 02 §8.2).
package query

import (
	"database/sql"

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
		var widget, dataset, plan sql.NullString
		if err := rows.Scan(&m.ID, &m.ConversationID, &m.Role, &m.Text, &m.Steps, &widget, &dataset, &plan, &m.CreatedAt); err != nil {
			return nil, err
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
