// Package query implements the natural-language pipeline (doc 10):
// question → LLM plan → validation/entity resolution → whitelisted SQL
// compilation → dataset → widget + reasoning steps.
package query

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"glowworm/internal/ai"
	"glowworm/internal/core"
	"glowworm/internal/core/periods"
	"glowworm/internal/core/widgets"
	"glowworm/internal/store"
	"glowworm/internal/util"
)

// Service runs queries.
type Service struct {
	DB       *store.DB
	Provider ai.Provider
	Clock    interface{ Today() string }
	Repo     ConversationRepo
}

// ConversationRepo persists conversations + messages (doc 02 §8.2).
type ConversationRepo interface {
	CreateConversation(title string) (*core.Conversation, error)
	AddMessage(conversationID, role, text string, steps []string, widget *string, dataset *string, plan *string) (*core.Message, error)
	ListConversations() ([]*core.Conversation, error)
	Messages(conversationID string) ([]*core.Message, error)
	// MessagesPage pages history backwards (doc 10 §8): the newest page when
	// before is nil, else the page ending before the "created_at|id" cursor.
	MessagesPage(conversationID string, before *string, limit int) ([]*core.Message, error)
	GetConversation(id string) (*core.Conversation, error)
	DeleteConversation(id string) error
}

// Plan is the validated query plan (doc 10 §3).
type Plan struct {
	V            int     `json:"v"`
	Metric       string  `json:"metric"`
	EntityKind   string  `json:"entity_kind,omitempty"`
	EntityID     string  `json:"entity_id,omitempty"`
	EntityName   string  `json:"entity_name,omitempty"`
	EntityMatch  string  `json:"entity_match,omitempty"`
	Breakdown    string  `json:"breakdown"`
	TimePreset   *string `json:"time_preset,omitempty"`
	From         string  `json:"from"`
	To           string  `json:"to"`
	AccountScope string  `json:"account_scope"`
	Sort         string  `json:"sort"`
	Limit        int     `json:"limit"`
	ChartHint    string  `json:"chart_hint,omitempty"`
	Title        string  `json:"title"`
}

// Answer is the full query response (doc 10 §2). Multiple widgets per
// answer are supported (doc 10 §8); Plan/Dataset refer to the first widget
// when the legacy plan pipeline produced the answer.
type Answer struct {
	Text    string            `json:"text"`
	Steps   []string          `json:"steps"`
	Widgets []*widgets.Widget `json:"widgets"`
	Plan    *Plan             `json:"plan,omitempty"`
	Dataset *widgets.Dataset  `json:"dataset,omitempty"`
}

// CompilePublic executes an already-validated plan (pinned widget refresh,
// doc 10 §11) — no LLM involved.
func (s *Service) CompilePublic(p *Plan) (*widgets.Dataset, string, error) {
	if err := s.resolveTime(p); err != nil {
		return nil, "", err
	}
	if err := s.validate(p); err != nil {
		return nil, "", err
	}
	return s.compile(p)
}

// Ask runs the full pipeline: the agent first, the legacy plan compiler as
// fallback. See AskStream for the progressive version.
func (s *Service) Ask(question string) (*Answer, error) {
	return s.AskStreamWindow(question, nil, nil)
}

// AskStream answers with the agent loop (query tool + rich text/widgets),
// streaming reasoning steps through onStep as they happen.
func (s *Service) AskStream(question string, onStep func(string)) (*Answer, error) {
	return s.AskStreamWindow(question, nil, onStep)
}

// AskStreamWindow is AskStream with the user's current view period. If the
// agent fails on its protocol, the legacy plan pipeline takes over (doc 10
// §2 stays as the fallback; a provider that is down cannot be rescued).
func (s *Service) AskStreamWindow(question string, win *TimeWindow, onStep func(string)) (*Answer, error) {
	if s.Provider == nil {
		return nil, &ClarifyError{Message: "Connect an AI provider in Settings to ask questions."}
	}
	question = strings.TrimSpace(question)
	if question == "" || len(question) > 500 {
		return nil, fmt.Errorf("query: empty or too long")
	}
	ans, err := s.AskAgentWindow(question, win, onStep)
	if err == nil {
		return ans, nil
	}
	var unavail *ai.ErrUnavailable
	if errors.As(err, &unavail) {
		return nil, err
	}
	ans2, err2 := s.askLegacy(question)
	if err2 != nil {
		return nil, err2
	}
	if onStep != nil {
		for _, st := range ans2.Steps {
			onStep(st)
		}
	}
	return ans2, nil
}

// askLegacy is the original plan pipeline (doc 10 §2): question → plan →
// whitelisted SQL → dataset → templated widget answer.
func (s *Service) askLegacy(question string) (*Answer, error) {
	plan, err := s.plan(question)
	if err != nil {
		return nil, err
	}
	if err := s.resolve(plan); err != nil {
		return nil, err
	}
	if err := s.resolveTime(plan); err != nil {
		return nil, err
	}
	if err := s.validate(plan); err != nil {
		return nil, err
	}
	ds, sqlDisplay, err := s.compile(plan)
	if err != nil {
		return nil, err
	}
	steps := s.steps(plan, sqlDisplay, ds)
	widget := s.present(plan, ds)
	text := s.answer(plan, ds)
	return &Answer{Text: text, Steps: steps, Widgets: []*widgets.Widget{widget}, Plan: plan, Dataset: ds}, nil
}

// UnmarshalJSON accepts both the nested entity shape ({entity:{kind,...}})
// and the flattened internal shape.
func (p *Plan) UnmarshalJSON(b []byte) error {
	type alias Plan
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*p = Plan(a)
	var nested struct {
		Entity *struct {
			Kind         string `json:"kind"`
			ID           string `json:"id"`
			ResolvedName string `json:"resolved_name"`
			Match        string `json:"match"`
		} `json:"entity"`
	}
	if err := json.Unmarshal(b, &nested); err == nil && nested.Entity != nil {
		p.EntityKind = nested.Entity.Kind
		p.EntityID = nested.Entity.ID
		p.EntityName = nested.Entity.ResolvedName
		p.EntityMatch = nested.Entity.Match
	}
	return nil
}

// MarshalJSON re-nests the entity (doc 10 §3 shape).
func (p Plan) MarshalJSON() ([]byte, error) {
	type alias Plan
	a := alias(p)
	if p.EntityKind != "" && p.EntityKind != "none" {
		a.EntityKind = ""
		a.EntityID = ""
		a.EntityName = ""
		a.EntityMatch = ""
		b, err := json.Marshal(a)
		if err != nil {
			return nil, err
		}
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		m["entity"] = map[string]any{
			"kind": p.EntityKind, "id": p.EntityID,
			"resolved_name": p.EntityName, "match": p.EntityMatch,
		}
		return json.Marshal(m)
	}
	return json.Marshal(a)
}

// TimeWindow is the view period the user is currently browsing (the global
// period selector). The agent restricts answers to it unless the question
// specifies a different range (doc 10 §2a).
type TimeWindow struct {
	From  string // YYYY-MM-DD, empty = unbounded
	To    string
	Label string // e.g. "Last 12 months" — shown in the prompt
}

// ClarifyError asks the user to disambiguate (doc 10 §9).
type ClarifyError struct {
	Message     string   `json:"message"`
	Suggestions []string `json:"suggestions"`
}

func (e *ClarifyError) Error() string { return e.Message }

// plan asks the LLM for a query plan (doc 10 §4).
func (s *Service) plan(question string) (*Plan, error) {
	cats, _ := s.nameList(`SELECT name FROM categories WHERE deleted_at IS NULL AND is_system = 0`)
	accounts, _ := s.nameList(`SELECT name FROM accounts WHERE deleted_at IS NULL`)
	merchants, _ := s.nameList(`SELECT COALESCE(NULLIF(merchant,''), description) FROM transactions
		WHERE deleted_at IS NULL GROUP BY 1 ORDER BY COUNT(*) DESC LIMIT 30`)
	started := time.Now()
	resp, usage, err := s.Provider.CompleteStructured(&ai.StructuredRequest{
		Capability: "query_planning",
		PromptJSON: map[string]any{
			"question":        question,
			"today":           s.Clock.Today(),
			"categories":      cats,
			"accounts":        accounts,
			"known_merchants": merchants,
		},
	})
	s.logCall("query_planning", started, usage, err)
	if err != nil {
		return nil, err
	}
	var p Plan
	if err := json.Unmarshal([]byte(resp), &p); err != nil {
		return nil, fmt.Errorf("query: bad plan from model: %w", err)
	}
	if p.Metric == "" {
		return nil, fmt.Errorf("query: plan has no metric")
	}
	return &p, nil
}

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

// resolve validates + fuzzy-resolves the entity against the user's own data
// (doc 10 §3 validation; never trusts the LLM's ids).
func (s *Service) resolve(p *Plan) error {
	if p.EntityKind == "" || p.EntityKind == "none" {
		p.EntityKind = ""
		return nil
	}
	name := strings.TrimSpace(p.EntityName)
	if name == "" {
		p.EntityKind = ""
		return nil
	}
	switch p.EntityKind {
	case "category":
		var id string
		err := s.DB.QueryRow(`SELECT id FROM categories WHERE name = ? COLLATE NOCASE AND deleted_at IS NULL`, name).Scan(&id)
		if err == nil {
			p.EntityID, p.EntityMatch = id, "exact"
			return nil
		}
		// Fuzzy.
		var candidates []string
		rows, e := s.DB.Query(`SELECT name FROM categories WHERE deleted_at IS NULL AND name LIKE ?`, "%"+name+"%")
		if e == nil {
			for rows.Next() {
				var n string
				_ = rows.Scan(&n)
				candidates = append(candidates, n)
			}
			rows.Close()
		}
		if len(candidates) == 1 {
			var id2 string
			_ = s.DB.QueryRow(`SELECT id FROM categories WHERE name = ?`, candidates[0]).Scan(&id2)
			p.EntityID, p.EntityName, p.EntityMatch = id2, candidates[0], "fuzzy"
			return nil
		}
		return &ClarifyError{Message: fmt.Sprintf("I couldn't find a category called “%s”.", name), Suggestions: candidates}
	case "account":
		var id string
		err := s.DB.QueryRow(`SELECT id FROM accounts WHERE name = ? COLLATE NOCASE AND deleted_at IS NULL`, name).Scan(&id)
		if err == nil {
			p.EntityID, p.EntityMatch = id, "exact"
			p.AccountScope = "single"
			return nil
		}
		return &ClarifyError{Message: fmt.Sprintf("I couldn't find an account called “%s”.", name)}
	case "merchant":
		var n int
		_ = s.DB.QueryRow(`SELECT COUNT(*) FROM transactions WHERE deleted_at IS NULL AND (merchant = ? OR description = ?)`, name, name).Scan(&n)
		if n > 0 {
			p.EntityID = name
			p.EntityMatch = "exact"
			return nil
		}
		var candidates []string
		rows, e := s.DB.Query(`SELECT DISTINCT COALESCE(NULLIF(merchant,''), description) FROM transactions
			WHERE deleted_at IS NULL AND (merchant LIKE ? OR description LIKE ?) LIMIT 5`, "%"+name+"%", "%"+name+"%")
		if e == nil {
			for rows.Next() {
				var m string
				_ = rows.Scan(&m)
				candidates = append(candidates, m)
			}
			rows.Close()
		}
		if len(candidates) == 1 {
			p.EntityID, p.EntityName, p.EntityMatch = candidates[0], candidates[0], "fuzzy"
			return nil
		}
		return &ClarifyError{Message: fmt.Sprintf("I couldn't find a merchant called “%s”.", name), Suggestions: candidates}
	default:
		p.EntityKind = ""
	}
	return nil
}

// resolveTime fills from/to from the preset (doc 10 §3).
func (s *Service) resolveTime(p *Plan) error {
	today := s.Clock.Today()
	tt, _ := time.Parse("2006-01-02", today)
	from, to, _ := periods.Resolve(pickString(p.TimePreset, "last-12m"), p.From, p.To, tt)
	if p.From == "" && p.To == "" && from == "" {
		// "all" preset on a metric question defaults to last-12m (doc 10 §4).
		from, to, _ = periods.Resolve(periods.Last12M, "", "", tt)
	}
	p.From, p.To = from, to
	return nil
}

func pickString(p *string, def string) string {
	if p == nil || *p == "" {
		return def
	}
	return *p
}

// validate clamps + normalises the plan (doc 10 §3 hard rules).
func (s *Service) validate(p *Plan) error {
	switch p.Metric {
	case "spend", "income", "net", "balance", "count", "list":
	default:
		return fmt.Errorf("query: unknown metric %q", p.Metric)
	}
	switch p.Breakdown {
	case "", "none":
		p.Breakdown = "none"
	case "month", "week", "day", "category", "account", "merchant":
	default:
		return fmt.Errorf("query: unknown breakdown %q", p.Breakdown)
	}
	if p.Metric == "balance" && p.Breakdown == "none" && p.EntityKind == "" {
		p.Breakdown = "month" // balance needs a series or an entity
	}
	if p.Limit <= 0 {
		p.Limit = 12
	}
	if p.Limit > 50 {
		p.Limit = 50
	}
	if p.AccountScope == "" {
		p.AccountScope = "all"
	}
	if p.Title == "" {
		p.Title = strings.ToUpper(p.Metric[:1]) + p.Metric[1:] + " summary"
	}
	if p.Sort == "" {
		p.Sort = "value_desc"
	}
	return nil
}

// compile executes the whitelisted query for (metric, breakdown) and builds
// the dataset (doc 10 §5 — fixed table, bound parameters only).
func (s *Service) compile(p *Plan) (*widgets.Dataset, string, error) {
	switch p.Metric {
	case "spend", "income", "net":
		return s.compileMoney(p)
	case "balance":
		return s.compileBalance(p)
	case "count":
		return s.compileCount(p)
	case "list":
		return s.compileList(p)
	}
	return nil, "", fmt.Errorf("query: unhandled metric")
}

func (s *Service) accountFilter(p *Plan) (string, []any) {
	switch p.AccountScope {
	case "savings":
		return ` AND t.account_id IN (SELECT id FROM accounts WHERE type = 'savings' AND deleted_at IS NULL)`, nil
	case "spending":
		return ` AND t.account_id IN (SELECT id FROM accounts WHERE type != 'savings' AND deleted_at IS NULL)`, nil
	case "single":
		return ` AND t.account_id = ?`, []any{p.EntityID}
	default:
		return "", nil
	}
}

func (s *Service) compileMoney(p *Plan) (*widgets.Dataset, string, error) {
	if p.Breakdown == "none" {
		// Single scalar: SELECT SUM.
		where, args := s.metricWhere(p)
		sign := signFor(p.Metric)
		sqlDisplay := fmt.Sprintf("SELECT %s(amount_minor) FROM tx WHERE %s", sumExpr(p.Metric), where)
		var total any
		err := s.DB.QueryRow(`SELECT COALESCE(SUM(`+sign+`amount_minor),0) FROM transactions t
			WHERE t.deleted_at IS NULL`+where, args...).Scan(&total)
		if err != nil {
			return nil, sqlDisplay, err
		}
		totalMinor := totalToInt(total)
		ds := &widgets.Dataset{
			Columns: []widgets.Column{{Key: "total", Label: p.Title, Type: "money"}},
			Rows:    [][]any{{float64(totalMinor)}},
		}
		ds.Meta.Currency = s.currency()
		ds.Meta.EntityLabel = entityLabel(p)
		ds.Meta.TotalMinor = &totalMinor
		ds.Meta.TimeRange.From, ds.Meta.TimeRange.To = p.From, p.To
		return ds, sqlDisplay, nil
	}
	dim, groupCol, labelCol := breakdownFor(p.Breakdown)
	where, args := s.metricWhere(p)
	sign := sumExpr(p.Metric)
	limit := p.Limit
	if p.Breakdown == "month" || p.Breakdown == "week" || p.Breakdown == "day" {
		limit = 0 // time series: no limit
	}
	sqlDisplay := fmt.Sprintf("SELECT %s, SUM(%s) FROM tx WHERE 1=1%s GROUP BY %s", dim, sign, strings.TrimPrefix(where, " AND 1=1"), groupCol)
	q := fmt.Sprintf(`SELECT %s AS bucket, COALESCE(SUM(%s),0) AS total FROM transactions t
		WHERE t.deleted_at IS NULL%s GROUP BY %s ORDER BY %s`, labelCol, sign, where, groupCol, orderFor(p, groupCol))
	qArgs := args
	if limit > 0 {
		q += fmt.Sprintf(` LIMIT %d`, limit)
	}
	rows, err := s.DB.Query(q, qArgs...)
	if err != nil {
		return nil, sqlDisplay, err
	}
	defer rows.Close()
	ds := &widgets.Dataset{
		Columns: []widgets.Column{
			{Key: "bucket", Label: bucketLabel(p.Breakdown), Type: "string"},
			{Key: "total", Label: entityLabel(p), Type: "money"},
		},
	}
	var total int64
	for rows.Next() {
		var bucket string
		var totalAny any
		if err := rows.Scan(&bucket, &totalAny); err != nil {
			return nil, sqlDisplay, err
		}
		v := totalToInt(totalAny)
		total += v
		ds.Rows = append(ds.Rows, []any{bucket, float64(v)})
	}
	if p.Sort == "value_desc" && (p.Breakdown == "category" || p.Breakdown == "merchant") {
		// already ordered
	}
	ds.Meta.Currency = s.currency()
	ds.Meta.EntityLabel = entityLabel(p)
	ds.Meta.TotalMinor = &total
	ds.Meta.TimeRange.From, ds.Meta.TimeRange.To = p.From, p.To
	ds.Meta.RowCount = len(ds.Rows)
	return ds, sqlDisplay, rows.Err()
}

func (s *Service) metricWhere(p *Plan) (string, []any) {
	var typeClause string
	switch p.Metric {
	case "spend":
		typeClause = ` AND t.type = 'expense'`
	case "income":
		typeClause = ` AND t.type = 'income'`
	default: // net: income minus spend
		typeClause = ` AND t.type != 'transfer'`
	}
	where := ` AND 1=1` + typeClause
	args := []any{}
	if p.From != "" {
		where += ` AND t.date >= ?`
		args = append(args, p.From)
	}
	if p.To != "" {
		where += ` AND t.date <= ?`
		args = append(args, p.To)
	}
	if p.EntityKind == "category" && p.EntityID != "" {
		where += ` AND t.category_id = ?`
		args = append(args, p.EntityID)
	}
	if p.EntityKind == "merchant" && p.EntityID != "" {
		where += ` AND COALESCE(NULLIF(t.merchant,''), t.description) = ?`
		args = append(args, p.EntityID)
	}
	acc, accArgs := s.accountFilter(p)
	where += acc
	args = append(args, accArgs...)
	return where, args
}

func signFor(metric string) string {
	switch metric {
	case "spend":
		return "-t."
	case "income":
		return "t."
	default:
		return "t."
	}
}

// sumExpr builds the SQL sum expression per metric (net folds spend+income).
func sumExpr(metric string) string {
	switch metric {
	case "spend":
		return "CASE WHEN t.type = 'expense' THEN -t.amount_minor ELSE 0 END"
	case "income":
		return "CASE WHEN t.type = 'income' THEN t.amount_minor ELSE 0 END"
	default:
		return "CASE WHEN t.type = 'income' THEN t.amount_minor ELSE -t.amount_minor END"
	}
}

func breakdownFor(b string) (dim, groupCol, labelCol string) {
	switch b {
	case "month":
		return "Month", `strftime('%Y-%m', t.date)`, `strftime('%Y-%m', t.date)`
	case "week":
		return "Week", `strftime('%Y-%W', t.date)`, `strftime('%Y-%W', t.date)`
	case "day":
		return "Day", `t.date`, `t.date`
	case "category":
		return "Category", `t.category_id`, `COALESCE((SELECT name FROM categories WHERE id = t.category_id), 'Uncategorised')`
	case "account":
		return "Account", `t.account_id`, `COALESCE((SELECT name FROM accounts WHERE id = t.account_id), 'Unknown')`
	default: // merchant
		return "Merchant", `COALESCE(NULLIF(t.merchant,''), t.description)`, `COALESCE(NULLIF(t.merchant,''), t.description)`
	}
}

func bucketLabel(b string) string {
	switch b {
	case "month":
		return "Month"
	case "week":
		return "Week"
	case "day":
		return "Day"
	case "category":
		return "Category"
	case "account":
		return "Account"
	default:
		return "Merchant"
	}
}

func entityLabel(p *Plan) string {
	if p.EntityName != "" {
		return p.EntityName
	}
	return strings.ToUpper(p.Metric[:1]) + p.Metric[1:]
}

func orderFor(p *Plan, groupCol string) string {
	if p.Breakdown == "month" || p.Breakdown == "week" || p.Breakdown == "day" {
		return "1 ASC"
	}
	return "2 DESC"
}

func totalToInt(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case float64:
		return int64(n)
	}
	return 0
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

// compileBalance uses the balance replay per month (doc 11 §5).
func (s *Service) compileBalance(p *Plan) (*widgets.Dataset, string, error) {
	if p.Breakdown == "none" {
		// Current balance for an account or totals.
		accounts := []string{}
		if p.EntityKind == "account" && p.EntityID != "" {
			accounts = append(accounts, p.EntityID)
		} else {
			rows, err := s.DB.Query(`SELECT id FROM accounts WHERE deleted_at IS NULL`)
			if err != nil {
				return nil, "", err
			}
			for rows.Next() {
				var id string
				_ = rows.Scan(&id)
				accounts = append(accounts, id)
			}
			rows.Close()
		}
		var total int64
		for _, id := range accounts {
			var opening int64
			var since any
			_ = s.DB.QueryRow(`SELECT opening_balance_minor, opening_balance_date FROM accounts WHERE id = ?`, id).Scan(&opening, &since)
			balance := opening
			sinceStr := ""
			if sd, ok := since.(string); ok {
				sinceStr = sd
			}
			q := `SELECT COALESCE(SUM(amount_minor),0) FROM transactions WHERE account_id = ? AND deleted_at IS NULL`
			args := []any{id}
			if sinceStr != "" {
				q += ` AND date > ?`
				args = append(args, sinceStr)
			}
			var sum any
			_ = s.DB.QueryRow(q, args...).Scan(&sum)
			balance += totalToInt(sum)
			total += balance
		}
		ds := &widgets.Dataset{
			Columns: []widgets.Column{{Key: "balance", Label: "Balance", Type: "money"}},
			Rows:    [][]any{{float64(total)}},
		}
		ds.Meta.Currency = s.currency()
		ds.Meta.EntityLabel = entityLabel(p)
		ds.Meta.TotalMinor = &total
		sqlDisplay := "SELECT SUM(balance) FROM accounts"
		return ds, sqlDisplay, nil
	}
	// Series: month-end balances over the range.
	from := p.From
	if len(from) > 7 {
		from = from[:7]
	}
	to := p.To
	if len(to) > 7 {
		to = to[:7]
	}
	months := monthKeys(from, to)
	ds := &widgets.Dataset{
		Columns: []widgets.Column{
			{Key: "month", Label: "Month", Type: "string"},
			{Key: "balance", Label: entityLabel(p), Type: "money"},
		},
	}
	sqlDisplay := "SELECT month, SUM(balance) FROM accounts GROUP BY month"
	for _, m := range months {
		end := monthEnd(m)
		var total int64
		var accIDs []string
		if p.EntityKind == "account" && p.EntityID != "" {
			accIDs = []string{p.EntityID}
		} else {
			rows, err := s.DB.Query(`SELECT id FROM accounts WHERE deleted_at IS NULL`)
			if err != nil {
				return nil, sqlDisplay, err
			}
			for rows.Next() {
				var id string
				_ = rows.Scan(&id)
				accIDs = append(accIDs, id)
			}
			rows.Close()
		}
		for _, id := range accIDs {
			var opening int64
			var since any
			_ = s.DB.QueryRow(`SELECT opening_balance_minor, opening_balance_date FROM accounts WHERE id = ?`, id).Scan(&opening, &since)
			balance := opening
			sinceStr := ""
			if sd, ok := since.(string); ok {
				sinceStr = sd
			}
			q := `SELECT COALESCE(SUM(amount_minor),0) FROM transactions WHERE account_id = ? AND deleted_at IS NULL AND date <= ?`
			args := []any{id, end}
			if sinceStr != "" {
				q += ` AND date > ?`
				args = append(args, sinceStr)
			}
			var sum any
			_ = s.DB.QueryRow(q, args...).Scan(&sum)
			balance += totalToInt(sum)
			total += balance
		}
		ds.Rows = append(ds.Rows, []any{m, float64(total)})
	}
	ds.Meta.Currency = s.currency()
	ds.Meta.EntityLabel = entityLabel(p)
	ds.Meta.TimeRange.From, ds.Meta.TimeRange.To = p.From, p.To
	ds.Meta.RowCount = len(ds.Rows)
	return ds, sqlDisplay, nil
}

func monthKeys(from, to string) []string {
	fy, fm := atoi(from[:4]), atoi(from[5:7])
	ty, tm := atoi(to[:4]), atoi(to[5:7])
	out := []string{}
	for y, m := fy, fm; y < ty || (y == ty && m <= tm); {
		out = append(out, fmt.Sprintf("%04d-%02d", y, m))
		m++
		if m > 12 {
			m = 1
			y++
		}
		if len(out) > 240 {
			break
		}
	}
	return out
}

func monthEnd(m string) string {
	y, mo := atoi(m[:4]), atoi(m[5:7])
	ny, nm := y, mo+1
	if nm > 12 {
		nm = 1
		ny++
	}
	end := fmt.Sprintf("%04d-%02d-01", ny, nm)
	t, _ := time.Parse("2006-01-02", end)
	return t.AddDate(0, 0, -1).Format("2006-01-02")
}

func atoi(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return n
		}
		n = n*10 + int(s[i]-'0')
	}
	return n
}

// compileCount counts transactions matching the plan.
func (s *Service) compileCount(p *Plan) (*widgets.Dataset, string, error) {
	where, args := s.metricWhere(p)
	where = strings.Replace(where, "type = 'expense'", "type != ''", 1)
	sqlDisplay := fmt.Sprintf("SELECT COUNT(*) FROM tx WHERE %s", where)
	var n int
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM transactions t WHERE t.deleted_at IS NULL`+where, args...).Scan(&n)
	if err != nil {
		return nil, sqlDisplay, err
	}
	nn := int64(n)
	ds := &widgets.Dataset{
		Columns: []widgets.Column{{Key: "count", Label: "Transactions", Type: "number"}},
		Rows:    [][]any{{float64(nn)}},
	}
	ds.Meta.Currency = s.currency()
	ds.Meta.EntityLabel = entityLabel(p)
	ds.Meta.TotalMinor = &nn
	return ds, sqlDisplay, nil
}

// compileList returns recent transaction display rows (doc 03 §5.1).
func (s *Service) compileList(p *Plan) (*widgets.Dataset, string, error) {
	limit := p.Limit
	if limit > 20 {
		limit = 20
	}
	q := `SELECT t.id FROM transactions t WHERE t.deleted_at IS NULL AND t.type != 'transfer'`
	args := []any{}
	if p.EntityKind == "category" && p.EntityID != "" {
		q += ` AND t.category_id = ?`
		args = append(args, p.EntityID)
	}
	q += ` ORDER BY t.date DESC LIMIT ` + fmt.Sprint(limit)
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		ids = append(ids, id)
	}
	items := []any{}
	for _, id := range ids {
		var description string
		var date string
		var amount int64
		var cat string
		_ = s.DB.QueryRow(`SELECT t.description, t.date, t.amount_minor, COALESCE(c.name,'Uncategorised')
			FROM transactions t LEFT JOIN categories c ON c.id = t.category_id WHERE t.id = ?`, id).
			Scan(&description, &date, &amount, &cat)
		items = append(items, map[string]any{
			"id": id, "date": date, "description": description,
			"amount_minor": amount, "category": cat, "type": "expense",
			"currency": s.currency(),
		})
	}
	ds := &widgets.Dataset{
		Columns: []widgets.Column{{Key: "transactions", Label: "Transactions", Type: "string"}},
		Rows:    [][]any{{items}},
	}
	ds.Meta.Currency = s.currency()
	sqlDisplay := fmt.Sprintf("SELECT * FROM tx ORDER BY date DESC LIMIT %d", limit)
	return ds, sqlDisplay, nil
}

// steps builds the reasoning highlights (doc 10 §7, prototype parity).
func (s *Service) steps(p *Plan, sqlDisplay string, ds *widgets.Dataset) []string {
	var out []string
	if p.EntityName != "" {
		marker := ""
		if p.EntityMatch == "fuzzy" {
			marker = " (closest match)"
		}
		out = append(out, fmt.Sprintf("Detected “%s” — %s%s", p.EntityName, entityKindLabel(p.EntityKind), marker))
	}
	switch p.AccountScope {
	case "savings":
		out = append(out, "Scoping to savings accounts")
	case "spending":
		out = append(out, "Scoping to spending accounts (excluding savings)")
	default:
		out = append(out, "Assuming all bank accounts")
	}
	out = append(out, "Generated query: "+sqlDisplay)
	if ds != nil && ds.Meta.Truncated {
		out = append(out, fmt.Sprintf("Rows truncated to %d", len(ds.Rows)))
	}
	return out
}

func entityKindLabel(kind string) string {
	switch kind {
	case "category":
		return "a category"
	case "account":
		return "an account"
	case "merchant":
		return "a merchant"
	}
	return "an entity"
}

// present selects the presentation (doc 10 §6). The plan rides along so the
// client can pin the widget with its resolved period (doc 05: pinned widgets
// store plans; the dashboard re-executes them).
func (s *Service) present(p *Plan, ds *widgets.Dataset) *widgets.Widget {
	w := s.presentBody(p, ds)
	w.ID = "w-" + util.NewID()
	w.Plan = p
	return w
}

func (s *Service) presentBody(p *Plan, ds *widgets.Dataset) *widgets.Widget {
	if p.Metric == "list" {
		return &widgets.Widget{Type: "transactions", Title: p.Title, Dataset: ds,
			Data: ds.Rows[0][0]}
	}
	if p.Breakdown == "none" || p.Breakdown == "" {
		return widgets.Derive("number", ds, p.Title)
	}
	kind := p.ChartHint
	if kind == "" {
		switch {
		case p.Breakdown == "month" || p.Breakdown == "week" || p.Breakdown == "day":
			kind = "line"
			if p.Metric == "spend" || p.Metric == "income" {
				kind = "bar"
			}
		case p.Breakdown == "category" || p.Breakdown == "merchant":
			if len(ds.Rows) <= 8 {
				kind = "pie"
			} else {
				kind = "bar"
			}
		default:
			kind = "bar"
		}
	}
	if kind == "pie" && len(ds.Rows) > 12 {
		kind = "bar"
	}
	return widgets.Derive(kind, ds, p.Title)
}

// answer builds the templated sentence (doc 10 §6 — no second LLM call).
func (s *Service) answer(p *Plan, ds *widgets.Dataset) string {
	if ds == nil {
		return "I couldn't compute that."
	}
	cur := ds.Meta.Currency
	rng := ""
	if ds.Meta.TimeRange.From != "" {
		rng = fmt.Sprintf(" between %s and %s", humanMonth(ds.Meta.TimeRange.From), humanMonth(ds.Meta.TimeRange.To))
	}
	switch p.Metric {
	case "spend":
		if ds.Meta.TotalMinor != nil {
			v := *ds.Meta.TotalMinor
			if v < 0 {
				v = -v
			}
			avg := ""
			if p.Breakdown == "month" && len(ds.Rows) > 0 {
				avg = fmt.Sprintf(" — averaging %s/month", money2(v/int64(len(ds.Rows)), cur))
			}
			return fmt.Sprintf("You spent %s on %s%s%s.", money2(v, cur), entityLabel(p), rng, avg)
		}
	case "income":
		if ds.Meta.TotalMinor != nil {
			return fmt.Sprintf("You received %s%s%s.", money2(*ds.Meta.TotalMinor, cur), entitySuffix(p), rng)
		}
	case "balance":
		if ds.Meta.TotalMinor != nil {
			return fmt.Sprintf("Your %s balance%s is %s.", entityLabel(p), entitySuffix(p), money2(*ds.Meta.TotalMinor, cur))
		}
	case "count":
		if ds.Meta.TotalMinor != nil {
			return fmt.Sprintf("That's %d transactions%s.", *ds.Meta.TotalMinor, rng)
		}
	case "list":
		return fmt.Sprintf("Here are your %d most recent transactions.", ds.Meta.RowCount)
	}
	return fmt.Sprintf("Here's your %s breakdown%s.", p.Metric, rng)
}

func entitySuffix(p *Plan) string {
	if p.EntityName != "" {
		return " for " + p.EntityName
	}
	return ""
}

func money2(minor int64, cur string) string {
	return moneyFmt(minor, cur)
}

func humanMonth(v string) string {
	if len(v) == 10 {
		t, err := time.Parse("2006-01-02", v)
		if err == nil {
			return t.Format("Jan 2006")
		}
	}
	if len(v) == 7 {
		t, err := time.Parse("2006-01", v)
		if err == nil {
			return t.Format("Jan 2006")
		}
	}
	return v
}

func moneyFmt(minor int64, currency string) string {
	// Delegate to the shared formatter to avoid duplication.
	return fmtMoney(minor, currency)
}
