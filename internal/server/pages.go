package server

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"glowworm/internal/core"
	"glowworm/internal/core/audit"
	"glowworm/internal/core/periods"
	"glowworm/internal/core/widgets"
	"glowworm/internal/money"
	"glowworm/internal/store/repos"
)

// queryMessageView renders one chat message server-side (M5 will populate).
type queryMessageView struct {
	Role       string
	Text       string
	DayLabel   string
	Steps      []string
	WidgetHTML template.HTML
}

// viewBase is the data every view shares (masthead, settings, period).
type viewBase struct {
	Title       string
	View        string // dashboard|queries|transactions|categories|import|history|settings
	Period      string
	PeriodLabel string
	PeriodFrom  string
	PeriodTo    string
	Currency    string
	Theme       string
	UserName    string
	Privacy     bool
	CSRFToken   string
	SetupOpen   bool // open the wizard on load
	SetupDone   bool
	AuthMode    string
	Palette     []string
	Standalone  bool
}

func (a *App) base(w http.ResponseWriter, r *http.Request, view, title string) (*viewBase, error) {
	s := a.Settings.Get()
	period := r.URL.Query().Get("period")
	if period == "" {
		period = s.DefaultPeriod
		if period == "" {
			period = "all"
		}
	}
	from, to, label := periods.Resolve(period, r.URL.Query().Get("from"), r.URL.Query().Get("to"), a.Clock.TodayTime())
	if label == "" {
		label = periods.Labels[periods.All]
	}
	return &viewBase{
		Title: title, View: view, Period: period, PeriodLabel: label,
		PeriodFrom: from, PeriodTo: to,
		Currency: s.Currency, Theme: s.Theme, UserName: s.UserName,
		Privacy: s.PrivacyDefault, CSRFToken: CSRFToken(w, r, a.Secret),
		SetupDone: s.SetupCompleted, AuthMode: a.Cfg.AuthMode,
		Palette: core.Palette,
	}, nil
}

// accountCard is the template projection of an account (doc 04 §5.2).
type accountCard struct {
	ID           string
	Name         string
	Type         string
	TypeLabel    string
	Color        string
	BalanceMinor int64
	BalanceNeg   bool
	RefLast4     string
	TxCount      int
	Corrections  int
	OpeningMinor int64
	OpeningDate  string
}

func (a *App) accountCards(counts map[string]int) ([]*accountCard, error) {
	list, err := a.Accounts.List(counts)
	if err != nil {
		return nil, err
	}
	out := make([]*accountCard, 0, len(list))
	for _, acct := range list {
		out = append(out, &accountCard{
			ID: acct.ID, Name: acct.Name, Type: acct.Type, TypeLabel: typeLabel(acct.Type),
			Color: acct.Color, BalanceMinor: acct.CurrentBalanceMinor, BalanceNeg: acct.CurrentBalanceMinor < 0,
			RefLast4: acct.RefLast4, TxCount: acct.TxCount, Corrections: acct.CorrectionsCount,
			OpeningMinor: acct.OpeningBalanceMinor, OpeningDate: acct.OpeningBalanceDate,
		})
	}
	return out, nil
}

func typeLabel(t string) string {
	switch t {
	case core.AccountSavings:
		return "Savings"
	case core.AccountCredit:
		return "Credit"
	default:
		return "Debit"
	}
}

// handleDashboard renders the dashboard view (doc 04 §5.1).
func (a *App) handleDashboard(w http.ResponseWriter, r *http.Request) error {
	base, err := a.base(w, r, "dashboard", "Dashboard")
	if err != nil {
		return err
	}
	counts, err := a.Repos.Transactions.CountByAccount(base.PeriodFrom, base.PeriodTo)
	if err != nil {
		return err
	}
	cards, err := a.accountCards(counts)
	if err != nil {
		return err
	}
	var available, savings int64
	for _, c := range cards {
		if c.Type == core.AccountSavings {
			savings += c.BalanceMinor
		} else {
			available += c.BalanceMinor
		}
	}

	// Income / spend this calendar month (doc 03 §3).
	incomeMinor, spendMinor := a.monthTotals()
	incomeTrendLabel, incomeTrendDate := a.lastIncomeTrend()
	spendPct, spendDir := a.spendTrend()

	// Hero chart series over the period.
	seriesData := a.balanceSeriesFor(base, "all")

	// Recent activity (grouped display, 7 rows).
	rows, err := a.Repos.Transactions.DisplayRows(repos.ListFilter{From: base.PeriodFrom, To: base.PeriodTo}, "")
	if err != nil {
		return err
	}
	recent := rows
	if len(recent) > 7 {
		recent = recent[:7]
	}
	widgetViews, err := a.WidgetsList()
	if err != nil {
		return err
	}

	data := struct {
		*viewBase
		Greeting        string
		Accounts        []*accountCard
		AvailableMinor  int64
		SavingsMinor    int64
		IncomeMinor     int64
		IncomeTrend     string
		IncomeTrendDate string
		SpendMinor      int64
		SpendPct        string
		SpendDir        string
		HeroChart       string // JSON chart data (bar/line)
		HeroRangeLabel  string
		Recent          []*repos.DisplayRow
		Widgets         []*WidgetView
	}{
		viewBase:       base,
		Greeting:       a.Clock.Greeting(),
		Accounts:       cards,
		AvailableMinor: available, SavingsMinor: savings,
		IncomeMinor: incomeMinor,
		IncomeTrend: incomeTrendLabel, IncomeTrendDate: incomeTrendDate,
		SpendMinor: spendMinor, SpendPct: spendPct, SpendDir: spendDir,
		HeroChart:      seriesData,
		HeroRangeLabel: "All accounts · " + lowerFirst(base.PeriodLabel),
		Recent:         recent,
		Widgets:        widgetViews,
	}
	return a.Render.Render(w, "dashboard", data)
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	b := []byte(s)
	if b[0] >= 'A' && b[0] <= 'Z' {
		b[0] += 32
	}
	return string(b)
}

// monthTotals computes income/spend for the current calendar month.
func (a *App) monthTotals() (income, spend int64) {
	from, to := periods.ThisMonth(a.Clock.TodayTime())
	var i, sp any
	_ = a.DB.QueryRow(`SELECT COALESCE(SUM(amount_minor),0) FROM transactions
		WHERE deleted_at IS NULL AND type='income' AND date BETWEEN ? AND ?`, from, to).Scan(&i)
	_ = a.DB.QueryRow(`SELECT COALESCE(SUM(-amount_minor),0) FROM transactions
		WHERE deleted_at IS NULL AND type='expense' AND date BETWEEN ? AND ?`, from, to).Scan(&sp)
	income, _ = i.(int64)
	spend, _ = sp.(int64)
	return income, spend
}

// lastIncomeTrend mirrors "Salary · 1 May": latest recurring income label+date.
func (a *App) lastIncomeTrend() (string, string) {
	var label, date string
	err := a.DB.QueryRow(`SELECT rk.label, t.date FROM transactions t
		JOIN recurring_keys rk ON rk.key = t.recurring_key
		WHERE t.deleted_at IS NULL AND t.type='income' AND t.recurring_key IS NOT NULL
		ORDER BY t.date DESC LIMIT 1`).Scan(&label, &date)
	if err != nil {
		var l2 string
		err2 := a.DB.QueryRow(`SELECT description, date FROM transactions
			WHERE deleted_at IS NULL AND type='income' ORDER BY date DESC LIMIT 1`).Scan(&l2, &date)
		if err2 != nil {
			return "", ""
		}
		return l2, date
	}
	return label, date
}

// spendTrend compares this month vs last (pct + direction).
func (a *App) spendTrend() (string, string) {
	_, to := periods.ThisMonth(a.Clock.TodayTime())
	pf, pt := periods.PrevMonth(a.Clock.TodayTime())
	var cur, prev any
	_ = a.DB.QueryRow(`SELECT COALESCE(SUM(-amount_minor),0) FROM transactions
		WHERE deleted_at IS NULL AND type='expense' AND date BETWEEN ? AND ?`, pf[:8]+"01", to).Scan(&cur)
	_ = a.DB.QueryRow(`SELECT COALESCE(SUM(-amount_minor),0) FROM transactions
		WHERE deleted_at IS NULL AND type='expense' AND date BETWEEN ? AND ?`, pf, pt).Scan(&prev)
	c, _ := cur.(int64)
	p, _ := prev.(int64)
	if p == 0 {
		return "", "flat"
	}
	pct := int(float64(c-p) / float64(p) * 100)
	sign := "+"
	dir := "up"
	if pct < 0 {
		sign = "−"
		pct = -pct
		dir = "down"
	}
	return fmt.Sprintf("%s%d%% vs last month", sign, pct), dir
}

// balanceSeriesFor returns JSON chart data for the hero chart (all scope).
func (a *App) balanceSeriesFor(base *viewBase, scope string) string {
	from := base.PeriodFrom
	if from == "" {
		var minDate any
		_ = a.DB.QueryRow(`SELECT MIN(date) FROM transactions WHERE deleted_at IS NULL`).Scan(&minDate)
		if md, ok := minDate.(string); ok && md != "" {
			from = md
		} else {
			from = time.Now().AddDate(-1, 0, 0).Format("2006-01-02")
		}
	}
	labels, values, err := balancesSeries(a, from, base.PeriodTo)
	if err != nil || len(labels) == 0 {
		return `{"labels":[],"series":[{"name":"Balance","color":"#0e6f51","values":[]}]}`
	}
	name, color := "Balance", "#0e6f51"
	return fmt.Sprintf(`{"labels":%s,"series":[{"name":%q,"color":%q,"values":%s}]}`,
		jsonSlice(labels), name, color, jsonInts(values))
}

func balancesSeries(a *App, from, to string) ([]string, []int64, error) {
	rows, err := a.DB.Query(`SELECT DISTINCT strftime('%Y-%m', date) AS m FROM transactions
		WHERE deleted_at IS NULL AND date BETWEEN ? AND ? ORDER BY m ASC`, from, to)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var labels []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, nil, err
		}
		labels = append(labels, m)
	}
	if len(labels) == 0 {
		return nil, nil, nil
	}
	values := make([]int64, len(labels))
	for i, m := range labels {
		end := monthEndOf(m)
		var accounts []string
		arows, err := a.DB.Query(`SELECT id FROM accounts WHERE deleted_at IS NULL`)
		if err != nil {
			return nil, nil, err
		}
		for arows.Next() {
			var id string
			_ = arows.Scan(&id)
			accounts = append(accounts, id)
		}
		arows.Close()
		for _, id := range accounts {
			var b int64
			b, err = balanceAt(a, id, end)
			if err != nil {
				return nil, nil, err
			}
			values[i] += b
		}
	}
	return labels, values, nil
}

func balanceAt(a *App, accountID, date string) (int64, error) {
	var opening int64
	var openingDate any
	if err := a.DB.QueryRow(`SELECT opening_balance_minor, opening_balance_date FROM accounts WHERE id=?`, accountID).
		Scan(&opening, &openingDate); err != nil {
		return 0, err
	}
	since := ""
	if s, ok := openingDate.(string); ok && s != "" {
		since = s
	}
	var balance int64
	var valDate any
	var val int64
	err := a.DB.QueryRow(`SELECT value_minor, date FROM valuations WHERE account_id=? AND date<=? AND deleted_at IS NULL
		ORDER BY date DESC, created_at DESC LIMIT 1`, accountID, date).Scan(&val, &valDate)
	if err == nil {
		balance = val
		if s, ok := valDate.(string); ok {
			since = s
		}
	}
	q := `SELECT COALESCE(SUM(amount_minor),0) FROM transactions WHERE account_id=? AND deleted_at IS NULL AND date <= ?`
	args := []any{accountID, date}
	if since != "" {
		q += ` AND date > ?`
		args = append(args, since)
	}
	var sum any
	if err := a.DB.QueryRow(q, args...).Scan(&sum); err != nil {
		return 0, err
	}
	if v, ok := sum.(int64); ok {
		balance += v
	}
	return balance, nil
}

func monthEndOf(m string) string {
	y := atoi(m[:4])
	mo := atoi(m[5:7])
	nextY, nextM := y, mo+1
	if nextM > 12 {
		nextM = 1
		nextY++
	}
	end := fmt.Sprintf("%04d-%02d-01", nextY, nextM)
	t, _ := time.Parse("2006-01-02", end)
	t = t.AddDate(0, 0, -1)
	return t.Format("2006-01-02")
}

func atoi(s string) int {
	n := 0
	for i := 0; i < len(s) && i < 4; i++ {
		n = n*10 + int(s[i]-'0')
	}
	return n
}

func jsonSlice(items []string) string {
	out := "["
	for i, s := range items {
		if i > 0 {
			out += ","
		}
		out += fmt.Sprintf("%q", s)
	}
	return out + "]"
}

func jsonInts(items []int64) string {
	out := "["
	for i, v := range items {
		if i > 0 {
			out += ","
		}
		out += strconv.FormatInt(v, 10)
	}
	return out + "]"
}

// handleTransactions renders the transactions view (doc 04 §5.2).
func (a *App) handleTransactions(w http.ResponseWriter, r *http.Request) error {
	base, err := a.base(w, r, "transactions", "Transactions")
	if err != nil {
		return err
	}
	q := r.URL.Query()
	f := repos.ListFilter{
		Q:          q.Get("q"),
		CategoryID: q.Get("category"),
		Type:       q.Get("type"),
		From:       base.PeriodFrom,
		To:         base.PeriodTo,
	}
	accountID := q.Get("account")
	counts, err := a.Repos.Transactions.CountByAccount(base.PeriodFrom, base.PeriodTo)
	if err != nil {
		return err
	}
	cards, err := a.accountCards(counts)
	if err != nil {
		return err
	}
	rows, err := a.Repos.Transactions.DisplayRows(f, accountID)
	if err != nil {
		return err
	}
	cats, err := a.Repos.Categories.List()
	if err != nil {
		return err
	}
	var selectedName, sub string
	for _, c := range cards {
		if c.ID == accountID {
			selectedName = c.Name
		}
	}
	if selectedName != "" {
		sub = selectedName + " · statement view"
	} else {
		sub = "All accounts · transfers linked"
	}
	data := struct {
		*viewBase
		Accounts     []*accountCard
		Rows         []*repos.DisplayRow
		Categories   []*core.Category
		SelectedAcc  string
		SelectedName string
		Sub          string
		FiltersQ     string
		FiltersCat   string
		FiltersType  string
		Today        string
	}{
		viewBase: base, Accounts: cards, Rows: rows, Categories: cats,
		SelectedAcc: accountID, SelectedName: selectedName, Sub: sub,
		FiltersQ: f.Q, FiltersCat: f.CategoryID, FiltersType: f.Type,
		Today: a.Clock.Today(),
	}
	return a.Render.Render(w, "transactions", data)
}

// handleCategories renders the categories view (doc 04 §5.5).
func (a *App) handleCategories(w http.ResponseWriter, r *http.Request) error {
	base, err := a.base(w, r, "categories", "Categories")
	if err != nil {
		return err
	}
	cats, err := a.Categories.List(base.PeriodFrom, base.PeriodTo)
	if err != nil {
		return err
	}
	var editing *core.Category
	if id := r.URL.Query().Get("edit"); id != "" {
		editing, _ = a.Repos.Categories.Get(id)
	}
	data := struct {
		*viewBase
		Categories []*core.Category
		Editing    *core.Category
	}{viewBase: base, Categories: cats, Editing: editing}
	return a.Render.Render(w, "categories", data)
}

// handleImport renders the import view (doc 04 §5.6).
func (a *App) handleImport(w http.ResponseWriter, r *http.Request) error {
	base, err := a.base(w, r, "import", "Import")
	if err != nil {
		return err
	}
	counts, _ := a.Repos.Transactions.CountByAccount(base.PeriodFrom, base.PeriodTo)
	cards, err := a.accountCards(counts)
	if err != nil {
		return err
	}
	cats, err := a.Repos.Categories.List()
	if err != nil {
		return err
	}
	schemas, err := a.Imports.ListSchemas()
	if err != nil {
		return err
	}
	data := struct {
		*viewBase
		Accounts   []*accountCard
		Schemas    []*core.ImportSchema
		Categories []*core.Category
	}{viewBase: base, Accounts: cards, Schemas: schemas, Categories: cats}
	return a.Render.Render(w, "import", data)
}

// handleHistory renders the history view (doc 04 §5.7).
func (a *App) handleHistory(w http.ResponseWriter, r *http.Request) error {
	base, err := a.base(w, r, "history", "History")
	if err != nil {
		return err
	}
	mode := r.URL.Query().Get("mode")
	if mode == "" {
		mode = "transactions"
	}
	filter := r.URL.Query().Get("filter")
	if filter == "" {
		filter = "all"
	}
	// Timeline months (min tx month → today).
	var minDate any
	_ = a.DB.QueryRow(`SELECT MIN(date) FROM transactions WHERE deleted_at IS NULL`).Scan(&minDate)
	start := time.Now().AddDate(-2, 0, 0).Format("2006-01-02")
	if md, ok := minDate.(string); ok && md != "" {
		start = md
	}
	labels, values, err := balancesSeries(a, start, a.Clock.Today())
	if err != nil {
		return err
	}
	months := labels
	latestBalance := int64(0)
	if len(values) > 0 {
		latestBalance = values[len(values)-1]
	}
	if len(months) == 0 {
		months = []string{a.Clock.Today()[:7]}
	}
	var txCount int64
	_ = a.DB.QueryRow(`SELECT COUNT(*) FROM transactions WHERE deleted_at IS NULL AND date <= ?`, a.Clock.Today()).Scan(&txCount)
	counts, _ := a.Repos.Transactions.CountByAccount("", "")
	cards, err := a.accountCards(counts)
	if err != nil {
		return err
	}
	opt := audit.ListOptions{Limit: 50}
	switch filter {
	case "you":
		opt.Actor = "user"
	case "ai":
		opt.Actor = "ai"
	case "import":
		opt.Action = "import"
	}
	entries, _, err := a.Audit.List(opt)
	if err != nil {
		return err
	}
	entryViews := toEntryViews(entries)
	for i := range entryViews {
		if caps, err := a.Audit.Capabilities(entryViews[i].ID); err == nil {
			entryViews[i].Revertible = caps.Revert
			entryViews[i].Jumpable = caps.JumpBack
			entryViews[i].Forgettable = caps.Forget
			entryViews[i].Deletable = caps.Delete
		}
	}
	reconStatus, reconLabel, reconHint := a.reconcileSummary()
	data := struct {
		*viewBase
		Mode            string
		AuditFilter     string
		Months          []string
		LatestDate      string
		LatestBalance   int64
		TxCountToDate   int64
		Accounts        []*accountCard
		Series          string
		Entries         []auditEntryView
		ReconcileStatus string
		ReconcileLabel  string
		ReconcileHint   string
	}{
		viewBase: base, Mode: mode, AuditFilter: filter, Months: months,
		LatestDate: a.Clock.Today(), LatestBalance: latestBalance,
		TxCountToDate: txCount, Accounts: cards,
		Series:          seriesJSON(labels, values),
		Entries:         entryViews,
		ReconcileStatus: reconStatus, ReconcileLabel: reconLabel, ReconcileHint: reconHint,
	}
	return a.Render.Render(w, "history", data)
}

// reconcileSummary aggregates the reconciliation card (doc 11 §6).
func (a *App) reconcileSummary() (string, string, string) {
	rows, err := a.DB.Query(`SELECT account_id, drift_minor FROM valuations
		WHERE deleted_at IS NULL AND date <= ?
		ORDER BY date ASC`, a.Clock.Today())
	if err != nil {
		return "unverified", "Unverified", "Record a current value in Settings to reconcile."
	}
	defer rows.Close()
	latestDrift := map[string]any{}
	for rows.Next() {
		var accountID string
		var drift any
		if err := rows.Scan(&accountID, &drift); err != nil {
			continue
		}
		latestDrift[accountID] = drift
	}
	hasVerified, hasDiverged := false, false
	for _, drift := range latestDrift {
		if d, ok := drift.(int64); ok && d != 0 {
			hasDiverged = true
		} else {
			hasVerified = true
		}
	}
	switch {
	case hasDiverged:
		return "diverged", "Divergences", "Review the affected account's transactions."
	case hasVerified:
		return "verified", "In sync", "No divergences"
	default:
		return "unverified", "Unverified", "Record a current value in Settings to reconcile."
	}
}

// handleSettings renders the settings view (doc 04 §5.8).
func (a *App) handleSettings(w http.ResponseWriter, r *http.Request) error {
	base, err := a.base(w, r, "settings", "Settings")
	if err != nil {
		return err
	}
	counts, _ := a.Repos.Transactions.CountByAccount("", "")
	cards, err := a.accountCards(counts)
	if err != nil {
		return err
	}
	s := a.Settings.Get()
	aiRow, _ := a.Repos.AIConfig.Get()
	aiMasked := ""
	if aiRow != nil && aiRow.Provider != "" {
		aiMasked = maskKey(aiRow.KeyCipher)
	}
	data := struct {
		*viewBase
		Accounts   []*accountCard
		AIProvider string
		AIKeyMask  string
		SetupDone  bool
	}{
		viewBase: base, Accounts: cards,
		AIProvider: aiProviderLabel(aiRow), AIKeyMask: aiMasked, SetupDone: s.SetupCompleted,
	}
	return a.Render.Render(w, "settings", data)
}

func aiProviderLabel(row *repos.AIConfigRow) string {
	if row == nil || row.Provider == "" {
		return ""
	}
	return row.Provider
}

func maskKey(cipher string) string {
	if cipher == "" {
		return ""
	}
	return "sk-••••••••••••••••"
}

// handleQueries renders the queries view (doc 04 §5.4).
func (a *App) handleQueries(w http.ResponseWriter, r *http.Request) error {
	base, err := a.base(w, r, "queries", "Queries")
	if err != nil {
		return err
	}
	convs, err := a.Query.Repo.ListConversations()
	if err != nil {
		return err
	}
	convID := r.URL.Query().Get("c")
	var conv *core.Conversation
	if convID == "" && len(convs) > 0 {
		conv = convs[0]
		convID = conv.ID
	} else if convID != "" {
		conv, err = a.Query.Repo.GetConversation(convID)
		if err != nil {
			conv = nil
			convID = ""
		}
	}
	var msgs []queryMessageView
	if conv != nil {
		raw, err := a.Query.Repo.Messages(conv.ID)
		if err == nil {
			msgs = toMessageViews(raw)
		}
	}
	data := struct {
		*viewBase
		Messages       []queryMessageView
		ConversationID string
		Conversations  []*core.Conversation
	}{viewBase: base, Messages: msgs, ConversationID: convID, Conversations: convs}
	return a.Render.Render(w, "queries", data)
}

// toMessageViews projects persisted messages (widget HTML re-rendered
// server-side from the stored widget JSON).
func toMessageViews(msgs []*core.Message) []queryMessageView {
	out := make([]queryMessageView, 0, len(msgs))
	lastDay := ""
	for _, m := range msgs {
		// Day badge only when the day changes (user request: not every message).
		d := dayKey(m.CreatedAt)
		var dayLabel string
		if d != lastDay {
			dayLabel = dayLabelFor(m.CreatedAt)
			lastDay = d
		}
		view := queryMessageView{Role: m.Role, Text: m.Text, Steps: m.Steps, DayLabel: dayLabel}
		if m.Widget != nil {
			var w widgets.Widget
			if json.Unmarshal([]byte(*m.Widget), &w) == nil {
				view.WidgetHTML = template.HTML(widgetCardHTML(&w))
			}
		}
		out = append(out, view)
	}
	return out
}

func dayLabelFor(ts string) string {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ""
	}
	today := strings.TrimSpace(time.Now().Format("2006-01-02"))
	that := strings.TrimSpace(t.Format("2006-01-02"))
	if today == that {
		return "Today"
	}
	if t.Format("2006-01-02") == time.Now().AddDate(0, 0, -1).Format("2006-01-02") {
		return "Yesterday"
	}
	return t.Format("2 January 2006")
}

// dayKey returns the calendar day of a timestamp (for change detection).
func dayKey(ts string) string {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ""
	}
	return t.Format("2006-01-02")
}

// widgetCardHTML re-renders a widget card server-side (prototype widgetEl).
func widgetCardHTML(w *widgets.Widget) string {
	var b strings.Builder
	b.WriteString(`<article class="widget span-12" data-widget-id="` + w.ID + `" data-widget-type="` + w.Type + `"`)
	b.WriteString(` data-widget-title="` + template.HTMLEscapeString(w.Title) + `"`)
	b.WriteString(` data-widget-data="` + template.HTMLEscapeString(w.DataJSON()) + `"`)
	if w.Dataset != nil {
		b.WriteString(` data-widget-dataset="` + template.HTMLEscapeString(marshalAny(w.Dataset)) + `"`)
	}
	b.WriteString(`><div class="widget-head"><div class="widget-title">` + template.HTMLEscapeString(w.Title) + `</div>`)
	b.WriteString(`<div class="widget-actions">
		<button class="widget-btn" data-action="toggle-pin" title="Pin to dashboard"><svg class="icon"><use href="#i-pin"/></svg></button>
		<button class="widget-btn" data-action="edit-widget" title="Widget settings"><svg class="icon"><use href="#i-settings"/></svg></button>
	</div></div><div class="widget-body">`)
	switch w.Type {
	case "number":
		d, _ := w.Data.(map[string]any)
		b.WriteString(`<div class="num-widget"><div class="num-value">` + template.HTMLEscapeString(str(d["value"])) + `</div>`)
		if d["caption"] != nil {
			b.WriteString(`<div class="num-caption">` + template.HTMLEscapeString(str(d["caption"])) + `</div>`)
		}
		b.WriteString(`</div>`)
	case "text":
		d, _ := w.Data.(map[string]any)
		b.WriteString(`<div class="text-widget"><p>` + template.HTMLEscapeString(str(d["body"])) + `</p></div>`)
	case "table":
		d, _ := w.Data.(map[string]any)
		b.WriteString(`<div class="table-widget"><table><thead><tr>`)
		if cols, ok := d["columns"].([]any); ok {
			for _, c := range cols {
				b.WriteString(`<th>` + template.HTMLEscapeString(str(c)) + `</th>`)
			}
		}
		b.WriteString(`</tr></thead><tbody>`)
		if rows, ok := d["rows"].([]any); ok {
			for _, r := range rows {
				b.WriteString(`<tr>`)
				if cells, ok := r.([]any); ok {
					for _, c := range cells {
						b.WriteString(`<td>` + template.HTMLEscapeString(str(c)) + `</td>`)
					}
				}
				b.WriteString(`</tr>`)
			}
		}
		b.WriteString(`</tbody></table></div>`)
	case "transactions":
		d, _ := w.Data.(map[string]any)
		if txs, ok := d["transactions"].([]any); ok {
			b.WriteString(`<div class="table-widget tx-widget"><table class="tx-table"><thead><tr><th>Date</th><th>Description</th><th>Category</th><th class="num">Amount</th></tr></thead><tbody>`)
			for _, t := range txs {
				tm, _ := t.(map[string]any)
				date := str(tm["date"])
				desc := str(tm["description"])
				cat := str(tm["category"])
				amount := int64(0)
				if f, ok := tm["amount_minor"].(float64); ok {
					amount = int64(f)
				}
				cur := str(tm["currency"])
				if cur == "" {
					cur = "GBP"
				}
				cls := "neg"
				if amount >= 0 {
					cls = "pos"
				}
				b.WriteString(`<tr><td class="cell-sub">` + template.HTMLEscapeString(dayMonth(date)) + `</td><td><div class="cell-merchant">` + template.HTMLEscapeString(desc) + `</div></td><td>` + template.HTMLEscapeString(cat) + `</td><td class="num tx-amount ` + cls + `">` + moneySignedCell(amount, cur) + `</td></tr>`)
			}
			b.WriteString(`</tbody></table></div>`)
		}
	default: // bar/line/pie render client-side via the widget engine
		typ := w.Type
		if typ != "pie" && typ != "line" && typ != "bar" {
			typ = "bar"
		}
		if typ == "pie" {
			b.WriteString(`<div class="pie-wrap"><div class="pie-chart" data-chart-type="pie" data-chart-data="` + template.HTMLEscapeString(w.DataJSON()) + `"></div></div>`)
		} else {
			b.WriteString(`<div class="chart-host" data-chart-type="` + typ + `" data-chart-data="` + template.HTMLEscapeString(w.DataJSON()) + `"></div>`)
		}
	}
	b.WriteString(`</div></article>`)
	return b.String()
}

func dayMonth(iso string) string {
	t, err := time.Parse("2006-01-02", iso)
	if err != nil {
		return iso
	}
	return t.Format("2 Jan")
}

func moneySignedCell(amount int64, cur string) string {
	cls := "neg"
	sign := "−"
	if amount >= 0 {
		cls = "pos"
		sign = "+"
	}
	return `<span class="` + cls + `">` + sign + money.Format(abs64(amount), cur) + `</span>`
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// moneyFmt is exposed for handlers composing labels.
func moneyFmt(minor int64, cc string) string { return money.Format(minor, cc) }
