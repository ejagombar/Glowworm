package query

import (
	"fmt"
	"strings"

	"glowworm/internal/ai"
	"glowworm/internal/core"
	"glowworm/internal/core/widgets"
	"glowworm/internal/store"
)

type Service struct {
	DB       *store.DB
	Provider ai.Provider
	Clock    interface{ Today() string }
	Repo     ConversationRepo
}

type ConversationRepo interface {
	CreateConversation(title string) (*core.Conversation, error)
	AddMessage(conversationID, role, text string, steps []string, widget *string) (*core.Message, error)
	ListConversations() ([]*core.Conversation, error)
	MessagesPage(conversationID string, before *string, limit int) ([]*core.Message, error)
	GetConversation(id string) (*core.Conversation, error)
}

type Answer struct {
	Text    string            `json:"text"`
	Steps   []string          `json:"steps"`
	Widgets []*widgets.Widget `json:"widgets"`
}

// Ask answers question over win, with history the earlier turns of the
// discussion (see RecentTurns). onStep, when set, is told each step as it
// happens.
func (s *Service) Ask(question string, history []Turn, win *TimeWindow, onStep func(string)) (*Answer, error) {
	if s.Provider == nil {
		return nil, &NoAnswerError{Message: "Connect an AI provider in Settings to ask questions."}
	}
	question = strings.TrimSpace(question)
	if question == "" || len(question) > 500 {
		return nil, fmt.Errorf("query: empty or too long")
	}
	return s.askAgent(question, history, win, onStep)
}

type TimeWindow struct {
	From  string
	To    string
	Label string
}

// NoAnswerError is why a question got no answer, worded for the person who
// asked it.
type NoAnswerError struct{ Message string }

func (e *NoAnswerError) Error() string { return e.Message }

func (s *Service) nameList(q string, args ...any) ([]string, error) {
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Service) currency() string {
	var cc string
	_ = s.DB.QueryRow(`SELECT value FROM settings WHERE key = 'currency'`).Scan(&cc)
	cc = strings.Trim(cc, `"`)
	if cc == "" {
		cc = "GBP"
	}
	return cc
}
