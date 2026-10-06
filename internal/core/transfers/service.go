package transfers

import (
	"database/sql"
	"fmt"
	"math"
	"sort"
	"time"

	"glowworm/internal/clock"
	"glowworm/internal/core"
	"glowworm/internal/core/audit"
	"glowworm/internal/core/normalize"
	"glowworm/internal/store"
	"glowworm/internal/store/repos"
)

type Service struct {
	DB          *store.DB
	Txs         *repos.Transactions
	Accounts    *repos.Accounts
	Audit       *audit.Writer
	Settings    *repos.Settings
	Clock       *clock.Clock
	InternalCat func() (string, error)
}

type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

const (
	dateWindowDays  = 4
	scoreIdentifier = 0.5
	scoreKeywords   = 0.3
	scoreAmount     = 0.3
	scoreDateClose  = 0.2
)

func (s *Service) Detect() (int, error) {
	threshold := s.Settings.GetFloat("transfer_threshold", 0.7)
	txs, err := s.Txs.UnlinkedTxs()
	if err != nil {
		return 0, err
	}
	idents := map[string][]string{}
	accounts, err := s.Accounts.List()
	if err != nil {
		return 0, err
	}
	for _, a := range accounts {
		fragments := []string{}
		for _, id := range []string{a.AccountNumber, a.SortCode, a.IBAN, a.BIC} {
			f := normalizeIdentifier(id)
			if len(f) >= 4 {
				fragments = append(fragments, f)
				if len(f) > 4 {
					fragments = append(fragments, f[len(f)-4:])
				}
			}
		}
		idents[a.ID] = fragments
	}
	lexicon := []string{"transfer", "tfr", "standing order", "faster payment", "to savings", "from savings",
		"to current", "from current", "credit card payment", "payment to", "payment from"}

	made := 0
	used := map[string]bool{}
	internalCat, err := s.InternalCat()
	if err != nil {
		return 0, err
	}
	for _, a := range txs {
		if used[a.ID] || a.Type == core.TypeTransfer {
			continue
		}
		best := (*core.Transaction)(nil)
		bestScore := 0.0
		bestMethod := ""
		for _, b := range txs {
			if b.ID == a.ID || used[b.ID] || b.Type == core.TypeTransfer {
				continue
			}
			if b.AccountID == a.AccountID {
				continue
			}
			if b.AmountMinor != -a.AmountMinor {
				continue
			}
			gapDays := dateGap(a.Date, b.Date)
			if gapDays > dateWindowDays {
				continue
			}
			score := scoreAmount
			method := "amount-date"
			if gapDays <= 1 {
				score += scoreDateClose
			}
			text := normalize.Text(a.Description + " " + b.Description + " " + a.SourceText + " " + a.DestinationText)
			if matchesIdentifier(text, idents[b.AccountID]) || matchesIdentifier(text, idents[a.AccountID]) {
				score += scoreIdentifier
				method = "identifier"
			}
			for _, kw := range lexicon {
				if strings_contains(text, kw) {
					score += scoreKeywords
					method = "description"
					break
				}
			}
			if score > bestScore {
				best, bestScore, bestMethod = b, score, method
			}
		}
		if best == nil || bestScore < threshold {
			continue
		}
		ties := 0
		for _, b := range txs {
			if b.ID == a.ID || used[b.ID] || b.AccountID == a.AccountID {
				continue
			}
			if b.AmountMinor != -a.AmountMinor && dateGap(a.Date, b.Date) <= dateWindowDays {
				continue
			}
			if b.AmountMinor == -a.AmountMinor {
				ties++
			}
		}
		if ties > 1 {
			continue
		}
		src, dst := a, best
		if src.AmountMinor > 0 {
			src, dst = dst, src
		}
		conf := bestScore
		tr := &core.Transfer{SourceTxID: src.ID, DestinationTxID: dst.ID,
			Detected: "auto", Method: bestMethod, Confidence: &conf}
		preSrcType, preSrcCat := src.Type, deref(src.CategoryID)
		preDstType, preDstCat := dst.Type, deref(dst.CategoryID)
		err := s.DB.WithTx(func(tx *sql.Tx) error {
			if err := s.Txs.CreateTransfer(tx, tr, src, dst, internalCat); err != nil {
				return err
			}
			_, err := s.Audit.Write(tx, audit.Input{
				Actor: core.ActorSystem, Action: "transfer.linked",
				EntityType: "transfer", EntityID: tr.ID,
				Title: fmt.Sprintf("Detected transfer: %s → %s", src.AccountName(), dst.AccountName()),
				Sub:   fmt.Sprintf("£%.2f · %s match", math.Abs(float64(src.AmountMinor))/100, bestMethod),
				Before: map[string]any{
					"legs": []map[string]any{
						{"id": src.ID, "type": preSrcType, "category_id": preSrcCat, "category_source": src.CategorySource},
						{"id": dst.ID, "type": preDstType, "category_id": preDstCat, "category_source": dst.CategorySource},
					},
				},
				After: map[string]any{
					"transfer_id": tr.ID,
					"legs": []map[string]any{
						{"id": src.ID, "type": core.TypeTransfer, "category_id": internalCat, "category_source": "none"},
						{"id": dst.ID, "type": core.TypeTransfer, "category_id": internalCat, "category_source": "none"},
					},
				},
				Revert: map[string]any{"transfer_id": tr.ID},
			})
			return err
		})
		if err != nil {
			return made, err
		}
		used[src.ID] = true
		used[dst.ID] = true
		made++
	}
	return made, nil
}

func normalizeIdentifier(s string) string {
	var b []rune
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b = append(b, r)
		}
	}
	return string(b)
}

func matchesIdentifier(text string, fragments []string) bool {
	for _, f := range fragments {
		if f != "" && strings_contains(text, f) {
			return true
		}
	}
	return false
}

func strings_contains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func dateGap(a, b string) int {
	ta, err1 := time.Parse("2006-01-02", a)
	tb, err2 := time.Parse("2006-01-02", b)
	if err1 != nil || err2 != nil {
		return 999
	}
	d := int(ta.Sub(tb).Hours() / 24)
	if d < 0 {
		return -d
	}
	return d
}

func (s *Service) Link(txID, counterpartID, actor string) (*core.Transfer, error) {
	if txID == counterpartID {
		return nil, &ValidationError{Message: "A transaction can't link to itself"}
	}
	a, err := s.Txs.Get(txID)
	if err != nil {
		return nil, err
	}
	b, err := s.Txs.Get(counterpartID)
	if err != nil {
		return nil, err
	}
	if a.TransferID != nil {
		return nil, &ValidationError{Message: "This transaction is already linked — break the link first"}
	}
	if b.TransferID != nil {
		return nil, &ValidationError{Message: "The counterpart is already linked to another transfer"}
	}
	if a.AccountID == b.AccountID {
		return nil, &ValidationError{Message: "Transfers link two different accounts"}
	}
	internalCat, err := s.InternalCat()
	if err != nil {
		return nil, err
	}
	src, dst := a, b
	if src.AmountMinor > 0 {
		src, dst = dst, src
	}
	if src.AmountMinor >= 0 || dst.AmountMinor <= 0 {
		return nil, &ValidationError{Message: "Transfer legs need opposite signs (money out and money in)"}
	}
	tr := &core.Transfer{SourceTxID: src.ID, DestinationTxID: dst.ID, Detected: "manual", Method: "manual"}
	preSrcType, preSrcCat := src.Type, deref(src.CategoryID)
	preDstType, preDstCat := dst.Type, deref(dst.CategoryID)
	err = s.DB.WithTx(func(tx *sql.Tx) error {
		if err := s.Txs.CreateTransfer(tx, tr, src, dst, internalCat); err != nil {
			return err
		}
		_, err := s.Audit.Write(tx, audit.Input{
			Actor: actor, Action: "transfer.linked",
			EntityType: "transfer", EntityID: tr.ID,
			Title: fmt.Sprintf("Linked transfer: %s → %s", src.AccountName(), dst.AccountName()),
			Sub:   fmt.Sprintf("%s · %s", src.Description, b.Description),
			Before: map[string]any{
				"legs": []map[string]any{
					{"id": src.ID, "type": preSrcType, "category_id": preSrcCat, "category_source": src.CategorySource},
					{"id": dst.ID, "type": preDstType, "category_id": preDstCat, "category_source": dst.CategorySource},
				},
			},
			After: map[string]any{
				"transfer_id": tr.ID,
				"legs": []map[string]any{
					{"id": src.ID, "type": core.TypeTransfer, "category_id": internalCat, "category_source": "none"},
					{"id": dst.ID, "type": core.TypeTransfer, "category_id": internalCat, "category_source": "none"},
				},
			},
			Revert: map[string]any{"transfer_id": tr.ID},
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	return tr, nil
}

func (s *Service) Unlink(txID string, actor string) error {
	t, err := s.Txs.Get(txID)
	if err != nil {
		return err
	}
	if t.TransferID == nil {
		return &ValidationError{Message: "This transaction is not linked"}
	}
	tr, err := s.Txs.TransferByID(*t.TransferID)
	if err != nil {
		return err
	}
	src, err := s.Txs.GetTx(tr.SourceTxID)
	if err != nil {
		return err
	}
	dst, err := s.Txs.GetTx(tr.DestinationTxID)
	if err != nil {
		return err
	}
	internalCat, err := s.InternalCat()
	if err != nil {
		return err
	}
	return s.DB.WithTx(func(tx *sql.Tx) error {
		if err := s.Txs.DeleteTransfer(tx, tr.ID); err != nil {
			return err
		}
		for _, leg := range []*core.Transaction{src, dst} {
			newType := core.TypeExpense
			if leg.AmountMinor > 0 {
				newType = core.TypeIncome
			}
			if _, err := tx.Exec(`UPDATE transactions SET type = ?, transfer_id = NULL, category_id = NULL,
				category_source = 'none', category_confidence = NULL, updated_at = ? WHERE id = ?`,
				newType, time.Now().UTC().Format(time.RFC3339), leg.ID); err != nil {
				return err
			}
		}
		_, err := s.Audit.Write(tx, audit.Input{
			Actor: actor, Action: "transfer.unlink",
			EntityType: "transfer", EntityID: tr.ID,
			Title: "Broke the transfer link",
			Sub:   fmt.Sprintf("%s · %s", src.Description, dst.Description),
			Before: map[string]any{
				"transfer_id": tr.ID,
				"legs": []map[string]any{
					{"id": src.ID, "type": "transfer", "category_id": internalCat, "category_source": "none"},
					{"id": dst.ID, "type": "transfer", "category_id": internalCat, "category_source": "none"},
				},
			},
			Revert: map[string]any{"transfer_id": tr.ID},
		})
		return err
	})
}

type Suggestion struct {
	Tx    *core.Transaction `json:"tx"`
	Score float64           `json:"score"`
	Days  int               `json:"days"`
}

func (s *Service) Suggestions(txID string) ([]*Suggestion, error) {
	t, err := s.Txs.Get(txID)
	if err != nil {
		return nil, err
	}
	from := shiftDate(t.Date, -30)
	to := shiftDate(t.Date, 30)
	candidates, err := s.Txs.UnlinkedCandidates(t.AccountID, from, to)
	if err != nil {
		return nil, err
	}
	out := []*Suggestion{}
	for _, c := range candidates {
		gap := dateGap(t.Date, c.Date)
		score := 0.0
		amountDiff := math.Abs(float64(c.AmountMinor + t.AmountMinor))
		amountRef := math.Max(math.Abs(float64(t.AmountMinor)), 1)
		if amountDiff == 0 {
			score += 2.0
		} else if amountDiff/amountRef <= 0.01 {
			score += 1.0
		}
		score += 1 - math.Min(float64(gap), 30)/30
		if c.Type == core.TypeTransfer || t.Type == core.TypeTransfer {
			continue
		}
		out = append(out, &Suggestion{Tx: c, Score: score, Days: gap})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if out[i].Days != out[j].Days {
			return out[i].Days < out[j].Days
		}
		return out[i].Tx.Date > out[j].Tx.Date
	})
	if len(out) > 50 {
		out = out[:50]
	}
	return out, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func shiftDate(date string, days int) string {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return date
	}
	return t.AddDate(0, 0, days).Format("2006-01-02")
}

var _ = normalize.Text

func (s *Service) CounterpartOf(txID string) (*core.Transaction, error) {
	t, err := s.Txs.Get(txID)
	if err != nil {
		return nil, err
	}
	if t.TransferID == nil {
		return nil, &ValidationError{Message: "This transaction is not linked"}
	}
	tr, err := s.Txs.TransferByID(*t.TransferID)
	if err != nil {
		return nil, err
	}
	other := tr.DestinationTxID
	if other == t.ID {
		other = tr.SourceTxID
	}
	return s.Txs.Get(other)
}
