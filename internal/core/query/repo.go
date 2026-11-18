package query

import (
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/ejagombar/Glowworm/internal/core"
	"github.com/ejagombar/Glowworm/internal/store"
	"github.com/ejagombar/Glowworm/internal/util"
)

type ConvRepo struct{ DB *store.DB }

func (r *ConvRepo) CreateConversation(title string) (*core.Conversation, error) {
	c := &core.Conversation{ID: util.NewID(), Title: title, CreatedAt: util.NowStr(), UpdatedAt: util.NowStr()}
	c.UpdatedAt = c.CreatedAt
	_, err := r.DB.Exec(`INSERT INTO conversations (id, title, created_at, updated_at) VALUES (?,?,?,?)`,
		c.ID, c.Title, c.CreatedAt, c.UpdatedAt)
	return c, err
}

func (r *ConvRepo) AddMessage(conversationID, role, text string, steps []string, widget *string) (*core.Message, error) {
	stepsJSON := "[]"
	if len(steps) > 0 {
		b, _ := json.Marshal(steps)
		stepsJSON = string(b)
	}
	m := &core.Message{ID: util.NewID(), ConversationID: conversationID, Role: role,
		Text: text, Steps: steps, Widget: widget, CreatedAt: util.NowStr()}
	_, err := r.DB.Exec(`INSERT INTO messages (id, conversation_id, role, text, steps, widget, created_at)
		VALUES (?,?,?,?,?,?,?)`,
		m.ID, conversationID, role, text, stepsJSON, derefP(widget), m.CreatedAt)
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

// The (created_at, id) tiebreak keeps pages stable: a question and its reply
// often share a created_at second.
func (r *ConvRepo) MessagesPage(conversationID string, before *string, limit int) ([]*core.Message, error) {
	if limit <= 0 {
		limit = 20
	}
	q := `SELECT id, conversation_id, role, text, steps, widget, created_at
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
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

func scanMessage(rows *sql.Rows) (*core.Message, error) {
	var m core.Message
	var steps, widget sql.NullString
	if err := rows.Scan(&m.ID, &m.ConversationID, &m.Role, &m.Text, &steps, &widget, &m.CreatedAt); err != nil {
		return nil, err
	}
	if steps.Valid && steps.String != "" {
		_ = json.Unmarshal([]byte(steps.String), &m.Steps)
	}
	if widget.Valid && widget.String != "" {
		w := widget.String
		m.Widget = &w
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
