package server

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"sort"
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
		HeroRangeLabel: "All accounts · all time",
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
	// The hero chart is the prototype's all-time balance view (doc 04 §4:
	// "Balance overview" — line chart over the full account history),
	// independent of the period selector (which drives the stat cards).
	from := seriesStart(a)
	to := a.Clock.Today()
	labels, values, err := balancesSeries(a, from, to)
	if err != nil || len(labels) == 0 {
		return `{"labels":[],"series":[{"name":"Balance","color":"#0e6f51","values":[]}]}`
	}
	name, color := "Balance", "#0e6f51"
	return fmt.Sprintf(`{"labels":%s,"series":[{"name":%q,"color":%q,"values":%s}]}`,
		jsonSlice(labels), name, color, jsonInts(values))
}

// seriesStart is the earliest date that matters for balance history: the
// earliest transaction or the earliest opening balance, whichever comes first.
func seriesStart(a *App) string {
	var txMin, opMin any
	_ = a.DB.QueryRow(`SELECT MIN(date) FROM transactions WHERE deleted_at IS NULL`).Scan(&txMin)
	_ = a.DB.QueryRow(`SELECT MIN(opening_balance_date) FROM accounts WHERE deleted_at IS NULL AND opening_balance_date <> ''`).Scan(&opMin)
	start := ""
	for _, v := range []any{txMin, opMin} {
		if s, ok := v.(string); ok && s != "" {
			if start == "" || s < start {
				start = s
			}
		}
	}
	if start == "" {
		start = a.Clock.Today()
	}
	return start
}

func balancesSeries(a *App, from, to string) ([]string, []int64, error) {
	return balancePointSeries(a, from, to)
}

// balancePointSeries builds the balance history at **transaction resolution**:
// one point on every day the balance can change (opening balances,
// valuations, transactions) plus today. Between points the balance is flat,
// so the line shows every individual transaction's up/down movement.
//
// The checkpoint replay (doc 02 §6) is swept in memory per account: each
// account's per-day transaction sums and valuations are fetched once, then
// replayed across the sorted point list. This keeps the query count constant
// per account instead of three per (account, day) — which dominated page
// load on long histories.
func balancePointSeries(a *App, from, to string) ([]string, []int64, error) {
	if from > to {
		from, to = to, from
	}
	dates := map[string]bool{}
	addDates := func(q string, args ...any) {
		rows, err := a.DB.Query(q, args...)
		if err != nil {
			return
		}
		defer rows.Close()
		for rows.Next() {
			var d any
			if err := rows.Scan(&d); err == nil {
				if s, ok := d.(string); ok && s >= from && s <= to {
					dates[s] = true
				}
			}
		}
	}
	addDates(`SELECT DISTINCT date FROM transactions WHERE deleted_at IS NULL AND date BETWEEN ? AND ?`, from, to)
	addDates(`SELECT DISTINCT opening_balance_date FROM accounts WHERE deleted_at IS NULL AND opening_balance_date BETWEEN ? AND ?`, from, to)
	addDates(`SELECT DISTINCT date FROM valuations WHERE deleted_at IS NULL AND date BETWEEN ? AND ?`, from, to)
	if a.Clock.Today() >= from && a.Clock.Today() <= to {
		dates[a.Clock.Today()] = true
	}
	if len(dates) == 0 {
		return nil, nil, nil
	}
	sorted := make([]string, 0, len(dates))
	for d := range dates {
		sorted = append(sorted, d)
	}
	sort.Strings(sorted)
	// Cap density: beyond ~370 points, keep every Nth day so the SVG stays sane.
	if len(sorted) > 370 {
		step := (len(sorted) + 369) / 370
		kept := []string{sorted[0]}
		for i := step; i < len(sorted); i += step {
			kept = append(kept, sorted[i])
		}
		if last := sorted[len(sorted)-1]; kept[len(kept)-1] != last {
			kept = append(kept, last)
		}
		sorted = kept
	}
	values := make([]int64, len(sorted))
	// All non-deleted accounts with their opening baselines.
	arows, err := a.DB.Query(`SELECT id, opening_balance_minor, COALESCE(opening_balance_date,'') FROM accounts WHERE deleted_at IS NULL`)
	if err != nil {
		return nil, nil, err
	}
	defer arows.Close()
	for arows.Next() {
		var id string
		var opening int64
		var openingDate string
		if err := arows.Scan(&id, &opening, &openingDate); err != nil {
			return nil, nil, err
		}
		txs, err := accountTxSums(a, id, to)
		if err != nil {
			return nil, nil, err
		}
		vals, err := accountValuations(a, id, to)
		if err != nil {
			return nil, nil, err
		}
		// Checkpoint replay swept across the point list (same semantics as
		// the per-day replay): the opening balance is always the baseline —
		// even when its date is empty or in the future (a future opening
		// date simply excludes every earlier transaction); the latest
		// valuation at or before the point replaces the checkpoint; only
		// transactions strictly after the checkpoint count.
		base := opening
		since := openingDate
		var txAfter int64
		txi, vi := 0, 0
		for i, d := range sorted {
			for vi < len(vals) && vals[vi].date <= d {
				base = vals[vi].value
				since = vals[vi].date
				txi, txAfter = 0, 0 // re-count: only transactions after the new checkpoint
				vi++
			}
			for txi < len(txs) && txs[txi].date <= d {
				if since == "" || txs[txi].date > since {
					txAfter += txs[txi].sum
				}
				txi++
			}
			values[i] += base + txAfter
		}
	}
	return sorted, values, nil
}

// txDay is one account's net transaction movement on a single day.
type txDay struct {
	date string
	sum  int64
}

// valuationPoint is one account's recorded value on a single day.
type valuationPoint struct {
	date  string
	value int64
}

// accountTxSums returns the account's per-day net movements up to `until`,
// ascending. No lower bound: checkpoints may reach back before the series
// start, mirroring the original per-day replay.
func accountTxSums(a *App, accountID, until string) ([]txDay, error) {
	rows, err := a.DB.Query(`SELECT date, SUM(amount_minor) FROM transactions
		WHERE account_id=? AND deleted_at IS NULL AND date<=? GROUP BY date ORDER BY date`, accountID, until)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []txDay{}
	for rows.Next() {
		var d any
		var sum int64
		if err := rows.Scan(&d, &sum); err != nil {
			return nil, err
		}
		if s, ok := d.(string); ok {
			out = append(out, txDay{date: s, sum: sum})
		}
	}
	return out, rows.Err()
}

// accountValuations returns the account's valuations up to `until`, ascending
// by date then creation (latest creation wins per day, as the old per-day
// `LIMIT 1` replay did).
func accountValuations(a *App, accountID, until string) ([]valuationPoint, error) {
	rows, err := a.DB.Query(`SELECT date, value_minor FROM valuations
		WHERE account_id=? AND deleted_at IS NULL AND date<=? ORDER BY date, created_at`, accountID, until)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []valuationPoint{}
	for rows.Next() {
		var d any
		var value int64
		if err := rows.Scan(&d, &value); err != nil {
			return nil, err
		}
		if s, ok := d.(string); ok {
			out = append(out, valuationPoint{date: s, value: value})
		}
	}
	return out, rows.Err()
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
	mode, filter := historyParams(r)
	data, err := a.historyData(base, mode, filter)
	if err != nil {
		return err
	}
	return a.Render.Render(w, "history", data)
}

// handleHistoryPane renders only the active history pane (transactions
// timeline or audit log) as a fragment. The client switches modes with this
// so the page itself appears instantly (skeleton) while the data loads.
func (a *App) handleHistoryPane(w http.ResponseWriter, r *http.Request) error {
	// The pane is injected client-side after mode/filter changes — never
	// serve a heuristically cached copy.
	w.Header().Set("Cache-Control", "no-store")
	base, err := a.base(w, r, "history", "History")
	if err != nil {
		return err
	}
	mode, filter := historyParams(r)
	data, err := a.historyData(base, mode, filter)
	if err != nil {
		return err
	}
	return a.Render.RenderFragment(w, "history-pane-content", data)
}

// historyParams reads the mode/filter from the query string.
func historyParams(r *http.Request) (mode, filter string) {
	mode = r.URL.Query().Get("mode")
	if mode != "audit" {
		mode = "transactions"
	}
	filter = r.URL.Query().Get("filter")
	if filter == "" {
		filter = "all"
	}
	return mode, filter
}

// historyData assembles everything the history view (or pane fragment) needs.
// The expensive part — the transaction-resolution balance series — is only
// computed for the transactions pane; audit mode never needs it.
func (a *App) historyData(base *viewBase, mode, filter string) (any, error) {
	months := []string{}
	latestBalance := int64(0)
	txCount := int64(0)
	cards := []*accountCard{}
	series := ""
	reconStatus, reconLabel, reconHint := "", "", ""
	if mode != "audit" {
		// Timeline months (min tx month → today).
		var minDate any
		_ = a.DB.QueryRow(`SELECT MIN(date) FROM transactions WHERE deleted_at IS NULL`).Scan(&minDate)
		start := time.Now().AddDate(-2, 0, 0).Format("2006-01-02")
		if md, ok := minDate.(string); ok && md != "" {
			start = md
		}
		labels, values, err := balancesSeries(a, start, a.Clock.Today())
		if err != nil {
			return nil, err
		}
		months = labels
		if len(values) > 0 {
			latestBalance = values[len(values)-1]
		}
		if len(months) == 0 {
			months = []string{a.Clock.Today()[:7]}
		}
		_ = a.DB.QueryRow(`SELECT COUNT(*) FROM transactions WHERE deleted_at IS NULL AND date <= ?`, a.Clock.Today()).Scan(&txCount)
		counts, _ := a.Repos.Transactions.CountByAccount("", "")
		cards, err = a.accountCards(counts)
		if err != nil {
			return nil, err
		}
		series = seriesJSON(labels, values)
		reconStatus, reconLabel, reconHint = a.reconcileSummary()
	}
	entryViews := []auditEntryView{}
	if mode == "audit" {
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
			return nil, err
		}
		entryViews = toEntryViews(entries)
		// Capability checks in one query + in-memory policy evaluation
		// (was one query per entry).
		if len(entries) > 0 {
			args := make([]any, len(entries))
			for i, e := range entries {
				args[i] = e.ID
			}
			rows, err := a.DB.Query(`SELECT id, action, state FROM audit_entries WHERE id IN (`+strings.Repeat("?,", len(entries)-1)+"?)", args...)
			if err == nil {
				states := map[int64]audit.AuditRow{}
				for rows.Next() {
					var row audit.AuditRow
					if err := rows.Scan(&row.ID, &row.Action, &row.State); err == nil {
						states[row.ID] = row
					}
				}
				rows.Close()
				for i := range entryViews {
					if row, ok := states[entryViews[i].ID]; ok {
						caps := a.Audit.Policy.Evaluate(row.Action, row.State)
						entryViews[i].Revertible = caps.Revert
						entryViews[i].Jumpable = caps.JumpBack
						entryViews[i].Forgettable = caps.Forget
						entryViews[i].Deletable = caps.Delete
					}
				}
			}
		}
	}
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
		Series:          series,
		Entries:         entryViews,
		ReconcileStatus: reconStatus, ReconcileLabel: reconLabel, ReconcileHint: reconHint,
	}
	return data, nil
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
		AIHasKey   bool
		AIBaseURL  string
		AIModel    string
		SetupDone  bool
	}{
		viewBase: base, Accounts: cards,
		AIProvider: aiProviderLabel(aiRow), AIKeyMask: aiMasked, SetupDone: s.SetupCompleted,
	}
	if aiRow != nil {
		data.AIHasKey = aiRow.KeyCipher != ""
		data.AIBaseURL = aiRow.BaseURL
		data.AIModel = aiRow.Models["default"]
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

// monthRange lists YYYY-MM keys from one month to another (inclusive).
func monthRange(from, to string) []string {
	if len(from) < 7 || len(to) < 7 {
		return nil
	}
	fy, fm := atoi(from[:4]), atoi(from[5:7])
	ty, tm := atoi(to[:4]), atoi(to[5:7])
	if fy == 0 || fm < 1 || fm > 12 || ty == 0 || tm < 1 || tm > 12 {
		return nil
	}
	var out []string
	for y, m := fy, fm; y < ty || (y == ty && m <= tm); {
		out = append(out, fmt.Sprintf("%04d-%02d", y, m))
		m++
		if m > 12 {
			m = 1
			y++
		}
		if len(out) >= 600 { // safety cap (~50 years)
			break
		}
	}
	return out
}
