package server

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"glowworm/internal/core"
	"glowworm/internal/core/accounts"
	"glowworm/internal/core/audit"
	"glowworm/internal/core/balances"
	"glowworm/internal/core/periods"
	"glowworm/internal/core/query"
	"glowworm/internal/core/widgets"
	"glowworm/internal/money"
	"glowworm/internal/store/repos"
)

type queryMessageView struct {
	Role       string
	Text       string
	TextHTML   template.HTML
	DayLabel   string
	Steps      []string
	WidgetHTML template.HTML
	CreatedAt  string
}

type viewBase struct {
	Title       string
	View        string
	Period      string
	PeriodLabel string
	PeriodFrom  string
	PeriodTo    string
	Currency    string
	Theme       string
	UserName    string
	Privacy     bool
	CSRFToken   string
	SetupDone   bool
	AuthMode    string
	Standalone  bool
	// Nonce marks the page's own inline scripts as allowed to run; see
	// contentSecurityPolicy.
	Nonce string
	// DataVersion is the data this page was built from; see dataVersion.
	DataVersion string
	// LockAfter is the app lock's time in minutes on a device that has it
	// on, for app.js to lock when the app comes back from being put away;
	// -1 when this device isn't locked.
	LockAfter int
}

// shortDate is "22 Oct 2025", matching the date range button in app.js.
func shortDate(iso string) string {
	t, err := time.Parse("2006-01-02", iso)
	if err != nil {
		return iso
	}
	return t.Format("2 Jan 2006")
}

// selectedPeriod is the date range chosen in the masthead. Choosing one puts
// it in the URL (?period=, plus from/to for a custom range), which is
// remembered in a cookie, so every other page and link keeps it until the
// user picks another. Without either, it's the default from Settings.
func (a *App) selectedPeriod(w http.ResponseWriter, r *http.Request) (period, from, to string) {
	q := r.URL.Query()
	if p := q.Get("period"); validPeriod(p) {
		period = p
		if p == periods.Custom {
			from, to = q.Get("from"), q.Get("to")
		}
		v := url.Values{"period": {period}}
		if from != "" || to != "" {
			v.Set("from", from)
			v.Set("to", to)
		}
		http.SetCookie(w, &http.Cookie{Name: periodCookie, Value: v.Encode(), Path: "/",
			MaxAge: 365 * 24 * 3600, HttpOnly: true, Secure: isTLS(r), SameSite: http.SameSiteLaxMode})
		return period, from, to
	}
	if c, err := r.Cookie(periodCookie); err == nil {
		if v, err := url.ParseQuery(c.Value); err == nil && validPeriod(v.Get("period")) {
			return v.Get("period"), v.Get("from"), v.Get("to")
		}
	}
	period = a.Settings.Get().DefaultPeriod
	if !validPeriod(period) {
		period = periods.All
	}
	return period, "", ""
}

// validPeriod is a choice the date range menu offers.
func validPeriod(p string) bool {
	_, ok := periods.Labels[p]
	return ok && p != periods.ThisMonthPreset
}

func (a *App) base(w http.ResponseWriter, r *http.Request, view, title string) (*viewBase, error) {
	s := a.Settings.Get()
	period, from, to := a.selectedPeriod(w, r)
	from, to, label := periods.Resolve(period, from, to, a.Clock.TodayTime())
	if label == "" {
		label = periods.Labels[periods.All]
	}
	// The date range button is drawn with its final label, as app.js would set
	// it, so it doesn't flicker from a placeholder on every page.
	if period == periods.Custom && from != "" && to != "" {
		label = shortDate(from) + " – " + shortDate(to)
	}
	privacy := s.PrivacyDefault
	if c, err := r.Cookie(privacyCookie); err == nil {
		privacy = c.Value == "1"
	}
	lockAfter := -1
	if sess, ok := a.session(r); ok && a.Cfg.AuthMode == "password" && a.lockRequired(r, sess) {
		lockAfter = a.lockAfter()
	}
	nonce := randomHex(16)
	w.Header().Set("Content-Security-Policy", contentSecurityPolicy(nonce))
	return &viewBase{
		LockAfter: lockAfter, Nonce: nonce,
		Title: title, View: view, Period: period, PeriodLabel: label,
		PeriodFrom: from, PeriodTo: to,
		Currency: s.Currency, Theme: s.Theme, UserName: s.UserName,
		Privacy: privacy, CSRFToken: CSRFToken(w, r, a.Secret),
		SetupDone: s.SetupCompleted, AuthMode: a.Cfg.AuthMode, DataVersion: a.version.String(),
	}, nil
}

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

	incomeMinor, spendMinor := a.monthTotals()
	incomeTrendLabel, incomeTrendDate := a.lastIncomeTrend()
	spendPct, spendDir := a.spendTrend()

	// The three charts are cut from one reading of the accounts' history.
	seriesFrom, seriesTo := seriesStart(a), a.Clock.Today()
	history, historyErr := loadBalanceHistory(a, max(seriesFrom, seriesTo))
	heroSeries := func(scope string) string {
		if historyErr != nil {
			return balanceSeriesJSON(scope, nil, nil)
		}
		labels, values := history.series(seriesFrom, seriesTo, a.Clock.Today(), scope)
		return balanceSeriesJSON(scope, labels, values)
	}
	seriesData := heroSeries("all")
	seriesAvailable := heroSeries("available")
	seriesSavings := heroSeries("savings")

	widgetViews, err := a.WidgetsList(base.Period, base.PeriodFrom, base.PeriodTo)
	if err != nil {
		return err
	}

	data := struct {
		*viewBase
		Accounts        []*accountCard
		AvailableMinor  int64
		SavingsMinor    int64
		IncomeMinor     int64
		IncomeTrend     string
		IncomeTrendDate string
		SpendMinor      int64
		SpendPct        string
		SpendDir        string
		HeroChart       string
		HeroAvailable   string
		HeroSavings     string
		Widgets         []*WidgetView
	}{
		viewBase:       base,
		Accounts:       cards,
		AvailableMinor: available, SavingsMinor: savings,
		IncomeMinor: incomeMinor,
		IncomeTrend: incomeTrendLabel, IncomeTrendDate: incomeTrendDate,
		SpendMinor: spendMinor, SpendPct: spendPct, SpendDir: spendDir,
		HeroChart:     seriesData,
		HeroAvailable: seriesAvailable,
		HeroSavings:   seriesSavings,
		Widgets:       widgetViews,
	}
	return a.Render.Render(w, "dashboard", data)
}

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

// balanceSeriesJSON is a balance chart's data for the dashboard: one line,
// named and coloured for its scope.
func balanceSeriesJSON(scope string, labels []string, values []int64) string {
	name, color := "Balance", "#0e6f51"
	switch scope {
	case "available":
		name = "Available"
	case "savings":
		name, color = "Savings", "#2f7fb0"
	}
	if len(labels) == 0 {
		return fmt.Sprintf(`{"labels":[],"series":[{"name":%q,"color":%q,"values":[]}]}`, name, color)
	}
	return fmt.Sprintf(`{"labels":%s,"series":[{"name":%q,"color":%q,"values":%s}]}`,
		jsonSlice(labels), name, color, jsonInts(values))
}

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

func balancePointSeries(a *App, from, to string) ([]string, []int64, error) {
	return balanceScopeSeries(a, from, to, "all")
}

// inScope narrows to the dashboard's split: savings accounts, or everything
// else ("available"). Any other scope takes every account.
func inScope(scope, accountType string) bool {
	switch scope {
	case "savings":
		return accountType == core.AccountSavings
	case "available":
		return accountType != core.AccountSavings
	}
	return true
}

// balanceScopeSeries is the combined balance of the accounts in scope on each
// day between from and to that something happened.
func balanceScopeSeries(a *App, from, to, scope string) ([]string, []int64, error) {
	if from > to {
		from, to = to, from
	}
	h, err := loadBalanceHistory(a, to)
	if err != nil {
		return nil, nil, err
	}
	labels, values := h.series(from, to, a.Clock.Today(), scope)
	return labels, values, nil
}

// balanceHistory is what the balance charts are drawn from: each account's
// opening balance, its corrections and its transactions totalled by day. It
// is read in three queries whatever the number of accounts, and one reading
// serves every scope a page shows.
type balanceHistory struct {
	accounts []*historyAccount
}

type historyAccount struct {
	accountType string
	// A removed account adds nothing to a balance, but the days its
	// transactions and corrections fall on are still points on the chart.
	deleted     bool
	opening     int64
	openingDate string
	txs         []txDay          // oldest first
	vals        []valuationPoint // oldest first; on one day, in the order entered
}

type txDay struct {
	date string
	sum  int64
}

type valuationPoint struct {
	date  string
	value int64
}

// loadBalanceHistory reads every account's history up to and including until.
func loadBalanceHistory(a *App, until string) (*balanceHistory, error) {
	h := &balanceHistory{}
	byID := map[string]*historyAccount{}
	arows, err := a.DB.Query(`SELECT id, type, deleted_at IS NOT NULL, opening_balance_minor, COALESCE(opening_balance_date,'') FROM accounts`)
	if err != nil {
		return nil, err
	}
	defer arows.Close()
	for arows.Next() {
		var id string
		acc := &historyAccount{}
		if err := arows.Scan(&id, &acc.accountType, &acc.deleted, &acc.opening, &acc.openingDate); err != nil {
			return nil, err
		}
		byID[id] = acc
		h.accounts = append(h.accounts, acc)
	}
	if err := arows.Err(); err != nil {
		return nil, err
	}

	trows, err := a.DB.Query(`SELECT account_id, date, SUM(amount_minor) FROM transactions
		WHERE deleted_at IS NULL AND date <= ? GROUP BY account_id, date ORDER BY account_id, date`, until)
	if err != nil {
		return nil, err
	}
	defer trows.Close()
	for trows.Next() {
		var id, date string
		var sum int64
		if err := trows.Scan(&id, &date, &sum); err != nil {
			return nil, err
		}
		if acc := byID[id]; acc != nil {
			acc.txs = append(acc.txs, txDay{date: date, sum: sum})
		}
	}
	if err := trows.Err(); err != nil {
		return nil, err
	}

	vrows, err := a.DB.Query(`SELECT account_id, date, value_minor FROM valuations
		WHERE deleted_at IS NULL AND date <= ? ORDER BY account_id, date, created_at`, until)
	if err != nil {
		return nil, err
	}
	defer vrows.Close()
	for vrows.Next() {
		var id, date string
		var value int64
		if err := vrows.Scan(&id, &date, &value); err != nil {
			return nil, err
		}
		if acc := byID[id]; acc != nil {
			acc.vals = append(acc.vals, valuationPoint{date: date, value: value})
		}
	}
	return h, vrows.Err()
}

// maxSeriesPoints is the most points a chart is given; a longer history is
// thinned evenly, keeping its first and last day.
const maxSeriesPoints = 370

// series is the combined balance of the accounts in scope on each day from
// from to to that has a transaction, a correction or an opening balance, and
// on today. An account's balance is its latest correction (or its opening
// balance) plus the transactions since.
func (h *balanceHistory) series(from, to, today, scope string) ([]string, []int64) {
	if from > to {
		from, to = to, from
	}
	within := func(d string) bool { return d >= from && d <= to }
	dates := map[string]bool{}
	for _, acc := range h.accounts {
		if !inScope(scope, acc.accountType) {
			continue
		}
		for _, t := range acc.txs {
			if within(t.date) {
				dates[t.date] = true
			}
		}
		for _, v := range acc.vals {
			if within(v.date) {
				dates[v.date] = true
			}
		}
		if !acc.deleted && within(acc.openingDate) {
			dates[acc.openingDate] = true
		}
	}
	if within(today) {
		dates[today] = true
	}
	if len(dates) == 0 {
		return nil, nil
	}
	sorted := make([]string, 0, len(dates))
	for d := range dates {
		sorted = append(sorted, d)
	}
	sort.Strings(sorted)
	if len(sorted) > maxSeriesPoints {
		step := (len(sorted) + maxSeriesPoints - 1) / maxSeriesPoints
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
	for _, acc := range h.accounts {
		if acc.deleted || !inScope(scope, acc.accountType) {
			continue
		}
		base := acc.opening
		since := acc.openingDate
		var txAfter int64
		txi, vi := 0, 0
		for i, d := range sorted {
			for vi < len(acc.vals) && acc.vals[vi].date <= d {
				base = acc.vals[vi].value
				since = acc.vals[vi].date
				txi, txAfter = 0, 0
				vi++
			}
			for txi < len(acc.txs) && acc.txs[txi].date <= d {
				if since == "" || acc.txs[txi].date > since {
					txAfter += acc.txs[txi].sum
				}
				txi++
			}
			values[i] += base + txAfter
		}
	}
	return sorted, values
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

const transactionsPageSize = 50

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
	allRows, err := a.Repos.Transactions.DisplayRows(f, accountID)
	if err != nil {
		return err
	}
	sortKey, sortDir := txSortParams(q)
	if sortKey != "" {
		repos.SortDisplayRows(allRows, sortKey, sortDir)
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
	rows := allRows
	hasMore := false
	if len(rows) > transactionsPageSize {
		rows = rows[:transactionsPageSize]
		hasMore = true
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
		HasMore      bool
		NextOffset   int
		Sort, Dir    string
	}{
		viewBase: base, Accounts: cards, Rows: rows, Categories: cats,
		SelectedAcc: accountID, SelectedName: selectedName, Sub: sub,
		FiltersQ: f.Q, FiltersCat: f.CategoryID, FiltersType: f.Type,
		Today: a.Clock.Today(), HasMore: hasMore, NextOffset: len(rows),
		Sort: sortKey, Dir: sortDir,
	}
	return a.Render.Render(w, "transactions", data)
}

// txSortParams reads the table's column sort; "" means the default (newest
// first).
func txSortParams(q url.Values) (key, dir string) {
	switch q.Get("sort") {
	case "date", "description", "category", "account", "amount":
		key = q.Get("sort")
	default:
		return "", ""
	}
	dir = "desc"
	if q.Get("dir") == "asc" {
		dir = "asc"
	}
	return key, dir
}

func (a *App) handleTransactionsRows(w http.ResponseWriter, r *http.Request) error {
	w.Header().Set("Cache-Control", "no-store")
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
	offset := 0
	if v := q.Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			offset = n
		}
	}
	limit := transactionsPageSize
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	allRows, err := a.Repos.Transactions.DisplayRows(f, accountID)
	if err != nil {
		return err
	}
	sortKey, sortDir := txSortParams(q)
	if sortKey != "" {
		repos.SortDisplayRows(allRows, sortKey, sortDir)
	}
	if offset >= len(allRows) {
		return nil
	}
	end := offset + limit
	if end > len(allRows) {
		end = len(allRows)
	}
	if end < len(allRows) {
		w.Header().Set("X-Has-More", "1")
	}
	w.Header().Set("X-Next-Offset", strconv.Itoa(end))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	for _, row := range allRows[offset:end] {
		if err := a.Render.RenderFragment(w, "tx-tr", map[string]any{
			"Row":      row,
			"Currency": base.Currency,
			"Selected": accountID,
		}); err != nil {
			return err
		}
	}
	return nil
}

// handleSavings is a placeholder until the savings page is built.
func (a *App) handleSavings(w http.ResponseWriter, r *http.Request) error {
	base, err := a.base(w, r, "savings", "Savings")
	if err != nil {
		return err
	}
	return a.Render.Render(w, "savings", struct{ *viewBase }{base})
}

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

func (a *App) handleHistory(w http.ResponseWriter, r *http.Request) error {
	base, err := a.base(w, r, "history", "History")
	if err != nil {
		return err
	}
	data, err := a.historyData(base, historyFilter(r), historyShow(r), true)
	if err != nil {
		return err
	}
	return a.Render.Render(w, "history", data)
}

func (a *App) handleHistoryPane(w http.ResponseWriter, r *http.Request) error {
	w.Header().Set("Cache-Control", "no-store")
	base, err := a.base(w, r, "history", "History")
	if err != nil {
		return err
	}
	data, err := a.historyData(base, historyFilter(r), historyShow(r), false)
	if err != nil {
		return err
	}
	return a.Render.RenderFragment(w, "history-audit", data)
}

// historyShow is how many audit entries the page was asked to list.
func historyShow(r *http.Request) int {
	if n, _ := strconv.Atoi(r.URL.Query().Get("show")); n > historyPage {
		return n
	}
	return historyPage
}

func historyFilter(r *http.Request) string {
	if f := r.URL.Query().Get("filter"); f != "" {
		return f
	}
	return "all"
}

// historyData builds the History page: the balance timeline (skipped when
// only the audit log is being refreshed) and the audit log.
func (a *App) historyData(base *viewBase, filter string, show int, timeline bool) (any, error) {
	months := []string{}
	latestBalance := int64(0)
	txCount := int64(0)
	cards := []*accountCard{}
	series := ""
	reconStatus, reconLabel, reconHint := "", "", ""
	if timeline {
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
		reconStatus, reconLabel, reconHint = a.reconcileSummary(a.Clock.Today())
	}
	importAccounts, err := a.Accounts.List(nil)
	if err != nil {
		return nil, err
	}
	opt := audit.ListOptions{}
	switch filter {
	case "you":
		opt.Actor = core.ActorUser
	case "system":
		opt.Actors = []string{core.ActorAI, core.ActorSystem}
	}
	entries, more, err := listHistory(a.Audit.List, opt, show)
	if err != nil {
		return nil, err
	}
	older := ""
	if more {
		older = fmt.Sprintf("/history?filter=%s&show=%d#audit", url.QueryEscape(filter), len(entries)+historyMore)
	}
	entryViews := a.toEntryViews(entries)
	a.markEditableImports(entryViews)
	latest := a.Audit.LatestRewindableID()
	for i := range entryViews {
		v := &entryViews[i]
		v.Undoable = a.Audit.Policy.Evaluate(v.Action, v.State).Revert
		if c := a.Audit.Policy.Evaluate(v.Action, "active"); !c.Revert && !c.Derived {
			v.Fixed = true
		}
		// Only where something after it would actually be undone.
		v.Rewindable = v.State == "active" && v.ID < latest
	}

	blocks := groupAuditEntries(entryViews)
	layoutRail(blocks, more, filter == "all")
	for i := range blocks {
		if b := &blocks[i]; b.Kind == "undone" {
			// What a Redo would set aside, so the page can show it first.
			// A block that can't be redone sets nothing aside; the Redo says why.
			if ids, err := a.Audit.RedoSetsAside(idsOf(b.Entries)); err == nil {
				b.SetsAside = idsCSV(ids)
			}
		}
	}

	data := struct {
		*viewBase
		AuditFilter     string
		Months          []string
		LatestDate      string
		LatestBalance   int64
		TxCountToDate   int64
		Accounts        []*accountCard
		Series          string
		Blocks          []auditBlock
		OlderHref       string
		ImportAccounts  []*core.Account
		ReconcileStatus string
		ReconcileLabel  string
		ReconcileHint   string
	}{
		viewBase: base, AuditFilter: filter, Months: months,
		LatestDate: a.Clock.Today(), LatestBalance: latestBalance,
		TxCountToDate: txCount, Accounts: cards,
		Series:          series,
		Blocks:          blocks,
		OlderHref:       older,
		ImportAccounts:  importAccounts,
		ReconcileStatus: reconStatus, ReconcileLabel: reconLabel, ReconcileHint: reconHint,
	}
	return data, nil
}

// reconcileSummary checks each account's latest recorded balance on or
// before date against what its transactions say it should be. A debit or
// credit account that disagrees has a missing, duplicate or wrong
// transaction. A savings account's deposits arrive as linked transfers, so
// what's left of its change since its last value is returns or money from
// outside; that's reported, but it's not a divergence.
func (a *App) reconcileSummary(date string) (string, string, string) {
	const unverifiedHint = "Record a statement balance for a current or credit account in Settings."
	rows, err := a.DB.Query(`SELECT v.id, v.account_id, v.value_minor, v.date, acc.name, acc.type, acc.currency,
			COALESCE(acc.opening_balance_date, ''),
			EXISTS (SELECT 1 FROM valuations e WHERE e.account_id = v.account_id AND e.deleted_at IS NULL
				AND e.id <> v.id AND (e.date < v.date OR (e.date = v.date AND e.created_at < v.created_at)))
		FROM valuations v JOIN accounts acc ON acc.id = v.account_id
		WHERE v.deleted_at IS NULL AND acc.deleted_at IS NULL AND v.date <= ?
			AND v.id = (SELECT l.id FROM valuations l WHERE l.account_id = v.account_id AND l.deleted_at IS NULL
				AND l.date <= ? ORDER BY l.date DESC, l.created_at DESC LIMIT 1)
		ORDER BY acc.name`, date, date)
	if err != nil {
		return "unverified", "Unverified", unverifiedHint
	}
	type checked struct {
		id, accountID, date, name, typ, currency, openingDate string
		value                                                 int64
		hasEarlier                                            bool
	}
	var all []checked
	for rows.Next() {
		var c checked
		if err := rows.Scan(&c.id, &c.accountID, &c.value, &c.date, &c.name, &c.typ, &c.currency, &c.openingDate, &c.hasEarlier); err != nil {
			rows.Close()
			return "unverified", "Unverified", unverifiedHint
		}
		all = append(all, c)
	}
	rows.Close()
	var diverged, matched, growth []string
	for _, c := range all {
		if c.currency == "" {
			c.currency = a.Settings.Get().Currency
		}
		expected, err := balances.Expected(a.DB.DB, c.accountID, c.date, c.id)
		if err != nil {
			continue
		}
		drift := c.value - expected
		if c.typ == core.AccountSavings {
			// A first value with nothing before it to compare against is the
			// starting point, not growth.
			if drift != 0 && (c.hasEarlier || (c.openingDate != "" && c.openingDate <= c.date)) {
				growth = append(growth, fmt.Sprintf("%s: %s from returns or outside money up to %s.", c.name, money.FormatSigned(drift, c.currency), longDate(c.date)))
			}
			continue
		}
		if drift != 0 {
			if drift < 0 {
				drift = -drift
			}
			diverged = append(diverged, fmt.Sprintf("%s's balance on %s is %s out from its transactions", c.name, longDate(c.date), money.Format(drift, c.currency)))
		} else {
			matched = append(matched, c.name)
		}
	}
	note := strings.Join(growth, " ")
	withNote := func(hint string) string {
		if note == "" {
			return hint
		}
		return hint + " " + note
	}
	switch {
	case len(diverged) > 0:
		label := "1 divergence"
		if len(diverged) > 1 {
			label = fmt.Sprintf("%d divergences", len(diverged))
		}
		return "diverged", label, withNote(strings.Join(diverged, "; ") + ". Look for a missing, duplicate or wrong transaction.")
	case len(matched) > 0:
		return "verified", "In sync", withNote(joinNames(matched) + " match their statements.")
	default:
		if note != "" {
			return "unverified", "Unverified", unverifiedHint + " " + note
		}
		return "unverified", "Unverified", unverifiedHint
	}
}

// longDate turns 2026-11-05 into 5 Nov 2026.
func longDate(iso string) string {
	t, err := time.Parse("2006-01-02", iso)
	if err != nil {
		return iso
	}
	return t.Format("2 Jan 2006")
}

// joinNames lists names as "A", "A and B" or "A, B and C".
func joinNames(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

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
	archives, err := a.Accounts.Archives()
	if err != nil {
		return err
	}
	cats, err := a.Categories.List(base.PeriodFrom, base.PeriodTo)
	if err != nil {
		return err
	}
	data := struct {
		*viewBase
		Categories     []*core.Category
		Username       string // what the owner signs in with; empty without sign-in
		Accounts       []*accountCard
		Archives       []*accounts.Archive
		AIProvider     string
		AIKeyMask      string
		AIHasKey       bool
		AIBaseURL      string
		AIQueriesModel string
		AICatModel     string
		SetupDone      bool
		Lock           *lockSettings
	}{
		viewBase: base, Categories: cats, Accounts: cards, Archives: archives,
		AIProvider: aiProviderLabel(aiRow), AIKeyMask: aiMasked, SetupDone: s.SetupCompleted,
	}
	if a.Cfg.AuthMode == "password" {
		if data.Lock, err = a.lockSettings(r); err != nil {
			return err
		}
		if sess, ok := a.session(r); ok {
			if u, err := a.Repos.Users.Get(sess.UserID); err == nil {
				data.Username = u.Username
			}
		}
	}
	if aiRow != nil {
		data.AIHasKey = aiRow.KeyCipher != ""
		data.AIBaseURL = aiRow.BaseURL
		data.AIQueriesModel = aiRow.Models[modelRoleQueries]
		data.AICatModel = aiRow.Models[modelRoleCategorisation]
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

func (a *App) handleQueries(w http.ResponseWriter, r *http.Request) error {
	base, err := a.base(w, r, "queries", "Queries")
	if err != nil {
		return err
	}
	days, err := a.Query.DayEntries()
	if err != nil {
		return err
	}
	missing := 0
	for _, d := range days {
		if !d.HasSummary {
			missing++
		}
	}
	if missing > 0 {
		go a.Query.CatchUpSummaries()
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
	const initialPage = 20
	var msgs []queryMessageView
	hasMore := false
	oldest := ""
	if conv != nil {
		if raw, err := a.Query.Repo.MessagesPage(conv.ID, nil, initialPage); err == nil && len(raw) > 0 {
			msgs = toMessageViews(raw)
			oldest = raw[0].CreatedAt + "|" + raw[0].ID
			hasMore = len(raw) == initialPage
		}
	}
	data := struct {
		*viewBase
		Messages         []queryMessageView
		ConversationID   string
		DayEntries       []*query.DayEntry
		MissingSummaries bool
		DayParam         string
		HasMore          bool
		Oldest           string
	}{viewBase: base, Messages: msgs, ConversationID: convID, DayEntries: days,
		MissingSummaries: missing > 0,
		DayParam:         r.URL.Query().Get("day"), HasMore: hasMore, Oldest: oldest}
	return a.Render.Render(w, "queries", data)
}

func toMessageViews(msgs []*core.Message) []queryMessageView {
	out := make([]queryMessageView, 0, len(msgs))
	lastDay := ""
	for _, m := range msgs {
		d := dayKey(m.CreatedAt)
		var dayLabel string
		if d != lastDay {
			dayLabel = dayLabelFor(m.CreatedAt)
			lastDay = d
		}
		view := queryMessageView{Role: m.Role, Text: m.Text, Steps: m.Steps, DayLabel: dayLabel, CreatedAt: m.CreatedAt}
		if m.Role == "assistant" {
			view.TextHTML = template.HTML(formatAssistantText(m.Text))
		}
		if m.Widget != nil && *m.Widget != "" {
			for _, w := range parseStoredWidgets(*m.Widget) {
				view.WidgetHTML += template.HTML(widgetCardHTML(w))
			}
		}
		out = append(out, view)
	}
	return out
}

// Day keys use the server's local timezone, not UTC: grouping by UTC day
// mislabelled late-evening messages for UTC+ users (duplicate Today badges).
func dayLabelFor(ts string) string {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ""
	}
	t = t.Local()
	today := time.Now().Local()
	switch {
	case sameDay(t, today):
		return "Today"
	case sameDay(t, today.AddDate(0, 0, -1)):
		return "Yesterday"
	}
	return t.Format("2 January 2006")
}

func sameDay(a, b time.Time) bool {
	aY, aM, aD := a.Date()
	bY, bM, bD := b.Date()
	return aY == bY && aM == bM && aD == bD
}

func dayKey(ts string) string {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ""
	}
	return t.Local().Format("2006-01-02")
}

func parseStoredWidgets(s string) []*widgets.Widget {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var arr []*widgets.Widget
	if err := json.Unmarshal([]byte(s), &arr); err != nil {
		return nil
	}
	return arr
}

func formatAssistantText(text string) string {
	esc := template.HTMLEscapeString(text)
	var b strings.Builder
	bold := false
	for _, part := range strings.Split(esc, "**") {
		if bold {
			b.WriteString("<strong>")
			b.WriteString(part)
			b.WriteString("</strong>")
		} else {
			b.WriteString(part)
		}
		bold = !bold
	}
	return b.String()
}

func widgetCardHTML(w *widgets.Widget) string {
	var b strings.Builder
	b.WriteString(`<article class="widget span-12" data-widget-id="` + template.HTMLEscapeString(w.ID) + `" data-widget-type="` + template.HTMLEscapeString(w.Type) + `"`)
	b.WriteString(` data-widget-title="` + template.HTMLEscapeString(w.Title) + `"`)
	b.WriteString(` data-widget-data="` + template.HTMLEscapeString(w.DataJSON()) + `"`)
	plan := ""
	if w.Plan != nil {
		if plan = marshalAny(w.Plan); plan == "null" {
			plan = ""
		} else {
			b.WriteString(` data-widget-plan="` + template.HTMLEscapeString(plan) + `"`)
		}
	}
	b.WriteString(`><div class="widget-head"><div class="widget-title">` + template.HTMLEscapeString(w.Title) + `</div>`)
	rules := w.Type == "group_rule" || w.Type == "group_rules" || w.Type == "change" || w.Type == "valuation" || w.Type == "link_transfers"
	if !rules {
		b.WriteString(`<div class="widget-actions">`)
		if refreshable(w.Type, plan) {
			b.WriteString(`
		<button class="widget-btn" data-action="toggle-pin" title="Pin to dashboard"><svg class="icon"><use href="#i-pin"/></svg></button>`)
		}
		b.WriteString(`
		<button class="widget-btn" data-action="edit-widget" title="Widget settings"><svg class="icon"><use href="#i-settings"/></svg></button>
	</div>`)
	}
	b.WriteString(`</div><div class="widget-body">`)
	switch w.Type {
	case "group_rules":
		b.WriteString(`<div class="rules-host" data-rules-kind="list"></div>`)
	case "group_rule":
		b.WriteString(`<div class="rules-host" data-rules-kind="draft" data-draft="` + template.HTMLEscapeString(w.DataJSON()) + `"></div>`)
	case "change":
		b.WriteString(`<div class="rules-host" data-rules-kind="change" data-draft="` + template.HTMLEscapeString(w.DataJSON()) + `"></div>`)
	case "valuation":
		b.WriteString(`<div class="rules-host" data-rules-kind="valuation" data-draft="` + template.HTMLEscapeString(w.DataJSON()) + `"></div>`)
	case "link_transfers":
		b.WriteString(`<div class="rules-host" data-rules-kind="link_transfers"></div>`)
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
	default:
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
