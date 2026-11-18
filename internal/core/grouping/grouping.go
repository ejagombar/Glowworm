// Package grouping puts transactions whose descriptions differ ("To Jaeren
// Rodrigues - rent and water", "Jaeren Rodrigues - Rent and water") into one
// named group, by rules the user creates. Groups exist only through their
// rules; the group a transaction falls in is kept in transactions.merchant,
// which Top merchants, search and the AI's queries already read.
package grouping

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/ejagombar/Glowworm/internal/core"
	"github.com/ejagombar/Glowworm/internal/core/audit"
	"github.com/ejagombar/Glowworm/internal/store"
	"github.com/ejagombar/Glowworm/internal/util"
)

const (
	MatchIs       = "is"
	MatchContains = "contains"
)

type Rule struct {
	ID        string `json:"id"`
	Group     string `json:"group"`
	Match     string `json:"match"`
	Text      string `json:"text"`
	CreatedAt string `json:"created_at"`
	// Count is how many transactions the rule places in its group.
	Count int `json:"count"`
}

type Service struct {
	DB    *store.DB
	Audit *audit.Writer
}

type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

// Simplify is the form rules compare: lower case, words only. "To Jaeren
// Rodrigues - Rent and water" becomes "to jaeren rodrigues rent and water".
func Simplify(s string) string {
	var b strings.Builder
	space := true
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			space = false
		} else if !space {
			b.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(b.String())
}

func (r *Rule) matches(simple string) bool {
	text := Simplify(r.Text)
	if r.Match == MatchIs {
		return simple == text
	}
	return strings.Contains(" "+simple+" ", " "+text+" ")
}

// groupFor is the group of the first rule that matches. Rules are in order
// of precedence: "is" before "contains", then longer text, then older.
func groupFor(rules []*Rule, description string) (string, *Rule) {
	simple := Simplify(description)
	for _, r := range rules {
		if r.matches(simple) {
			return r.Group, r
		}
	}
	return "", nil
}

func (s *Service) rules(q interface {
	Query(string, ...any) (*sql.Rows, error)
}) ([]*Rule, error) {
	rows, err := q.Query(`SELECT id, group_name, match, text, created_at FROM group_rules WHERE deleted_at IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Rule
	for rows.Next() {
		r := &Rule{}
		if err := rows.Scan(&r.ID, &r.Group, &r.Match, &r.Text, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Match != b.Match {
			return a.Match == MatchIs
		}
		if la, lb := len(Simplify(a.Text)), len(Simplify(b.Text)); la != lb {
			return la > lb
		}
		return a.CreatedAt < b.CreatedAt
	})
	return out, rows.Err()
}

// List is every rule, by group, with how many transactions it places.
func (s *Service) List() ([]*Rule, error) {
	rules, err := s.rules(s.DB)
	if err != nil {
		return nil, err
	}
	descs, err := s.descriptions()
	if err != nil {
		return nil, err
	}
	for _, d := range descs {
		if _, r := groupFor(rules, d.text); r != nil {
			r.Count += d.n
		}
	}
	sort.SliceStable(rules, func(i, j int) bool {
		return strings.ToLower(rules[i].Group) < strings.ToLower(rules[j].Group)
	})
	return rules, nil
}

type descCount struct {
	text  string
	n     int
	group string
}

func (s *Service) descriptions() ([]descCount, error) {
	rows, err := s.DB.Query(`SELECT description, COUNT(*), COALESCE(MAX(merchant), '') FROM transactions
		WHERE deleted_at IS NULL GROUP BY description ORDER BY COUNT(*) DESC, description`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []descCount
	for rows.Next() {
		var d descCount
		if err := rows.Scan(&d.text, &d.n, &d.group); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Draft is rules not yet created, for one group.
type Draft struct {
	Group string      `json:"group"`
	Rules []DraftRule `json:"rules"`
}

type DraftRule struct {
	Match string `json:"match"`
	Text  string `json:"text"`
}

// Matched is a description a draft would put in its group, with how many
// transactions have it and the group it's in now, if any.
type Matched struct {
	Description string `json:"description"`
	Count       int    `json:"count"`
	Current     string `json:"current,omitempty"`
}

type Preview struct {
	Draft
	Matched []Matched `json:"matched"`
	// Created is true once every rule in the draft exists.
	Created bool `json:"created"`
}

func (d *Draft) check() error {
	d.Group = strings.TrimSpace(d.Group)
	if d.Group == "" {
		return &ValidationError{Message: "A group needs a name"}
	}
	if len(d.Group) > 60 {
		return &ValidationError{Message: "Group names are at most 60 characters"}
	}
	if len(d.Rules) == 0 {
		return &ValidationError{Message: "A group needs at least one rule"}
	}
	for i := range d.Rules {
		r := &d.Rules[i]
		r.Match = strings.ToLower(strings.TrimSpace(r.Match))
		r.Text = strings.TrimSpace(r.Text)
		if r.Match != MatchIs && r.Match != MatchContains {
			return &ValidationError{Message: fmt.Sprintf("A rule matches descriptions that are or contain some text, not %q", r.Match)}
		}
		if simple := Simplify(r.Text); simple == "" || (r.Match == MatchContains && len(simple) < 3) {
			return &ValidationError{Message: fmt.Sprintf("%q is too short to match on", r.Text)}
		}
	}
	return nil
}

// Preview says what creating the draft would do, without doing it.
func (s *Service) Preview(d Draft) (*Preview, error) {
	if err := d.check(); err != nil {
		return nil, err
	}
	existing, err := s.rules(s.DB)
	if err != nil {
		return nil, err
	}
	draft := make([]*Rule, len(d.Rules))
	for i, r := range d.Rules {
		draft[i] = &Rule{Group: d.Group, Match: r.Match, Text: r.Text}
	}
	descs, err := s.descriptions()
	if err != nil {
		return nil, err
	}
	p := &Preview{Draft: d, Matched: []Matched{}, Created: true}
	for _, r := range draft {
		if s.find(existing, r) == nil {
			p.Created = false
		}
	}
	for _, dc := range descs {
		if g, _ := groupFor(draft, dc.text); g == "" {
			continue
		}
		cur := dc.group
		if cur == d.Group {
			cur = ""
		}
		p.Matched = append(p.Matched, Matched{Description: dc.text, Count: dc.n, Current: cur})
	}
	return p, nil
}

func (s *Service) find(rules []*Rule, r *Rule) *Rule {
	for _, e := range rules {
		if e.Match == r.Match && Simplify(e.Text) == Simplify(r.Text) {
			return e
		}
	}
	return nil
}

// Create adds the draft's rules (skipping any that already exist) and
// regroups transactions.
func (s *Service) Create(d Draft) ([]*Rule, error) {
	if err := d.check(); err != nil {
		return nil, err
	}
	var created []*Rule
	err := s.DB.WithTx(func(tx *sql.Tx) error {
		existing, err := s.rules(tx)
		if err != nil {
			return err
		}
		for _, dr := range d.Rules {
			r := &Rule{ID: util.NewID(), Group: d.Group, Match: dr.Match, Text: dr.Text, CreatedAt: util.NowStr()}
			if e := s.find(existing, r); e != nil {
				if e.Group != d.Group {
					return &ValidationError{Message: fmt.Sprintf("Descriptions that %s %q are already grouped as %q", e.Match, e.Text, e.Group)}
				}
				continue
			}
			if _, err := tx.Exec(`INSERT INTO group_rules (id, group_name, match, text, created_at) VALUES (?,?,?,?,?)`,
				r.ID, r.Group, r.Match, r.Text, r.CreatedAt); err != nil {
				return err
			}
			if _, err := s.Audit.Write(tx, audit.Input{
				Actor: core.ActorUser, Action: "group_rule.created",
				EntityType: "group_rule", EntityID: r.ID,
				Title: "Created grouping rule", Sub: fmt.Sprintf("%s · description %s “%s”", r.Group, r.Match, r.Text),
				After:  map[string]any{"group": r.Group, "match": r.Match, "text": r.Text},
				Revert: map[string]any{"rule_id": r.ID},
			}); err != nil {
				return err
			}
			existing = append(existing, r)
			created = append(created, r)
		}
		return s.apply(tx)
	})
	return created, err
}

// Delete removes a rule; a group left with no rules is gone with it.
func (s *Service) Delete(id string) error {
	return s.DB.WithTx(func(tx *sql.Tx) error {
		var r Rule
		err := tx.QueryRow(`SELECT id, group_name, match, text FROM group_rules WHERE id = ? AND deleted_at IS NULL`, id).
			Scan(&r.ID, &r.Group, &r.Match, &r.Text)
		if errors.Is(err, sql.ErrNoRows) {
			return &ValidationError{Message: "That rule no longer exists"}
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE group_rules SET deleted_at = ? WHERE id = ?`, util.NowStr(), id); err != nil {
			return err
		}
		if _, err := s.Audit.Write(tx, audit.Input{
			Actor: core.ActorUser, Action: "group_rule.deleted",
			EntityType: "group_rule", EntityID: r.ID,
			Title: "Deleted grouping rule", Sub: fmt.Sprintf("%s · description %s “%s”", r.Group, r.Match, r.Text),
			Before: map[string]any{"group": r.Group, "match": r.Match, "text": r.Text},
			Revert: map[string]any{"rule_id": r.ID},
		}); err != nil {
			return err
		}
		return s.apply(tx)
	})
}

// Apply regroups every transaction by the current rules.
func (s *Service) Apply() error {
	return s.DB.WithTx(s.apply)
}

func (s *Service) apply(tx *sql.Tx) error {
	rules, err := s.rules(tx)
	if err != nil {
		return err
	}
	rows, err := tx.Query(`SELECT id, description, COALESCE(merchant, '') FROM transactions WHERE deleted_at IS NULL`)
	if err != nil {
		return err
	}
	type change struct{ id, group string }
	var changes []change
	for rows.Next() {
		var id, desc, cur string
		if err := rows.Scan(&id, &desc, &cur); err != nil {
			rows.Close()
			return err
		}
		if g, _ := groupFor(rules, desc); g != cur {
			changes = append(changes, change{id, g})
		}
	}
	rows.Close()
	for _, c := range changes {
		if _, err := tx.Exec(`UPDATE transactions SET merchant = NULLIF(?, '') WHERE id = ?`, c.group, c.id); err != nil {
			return err
		}
	}
	return nil
}
