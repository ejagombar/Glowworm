package audit

import "strings"

type Policy struct{}

// derivedActions are system bookkeeping recomputed from the data itself.
// They follow the change that caused them and can't be undone on their own.
var derivedActions = map[string]bool{
	"classifier.retrained": true,
	"recurring.detected":   true,
	"merchant.enriched":    true,
}

func IsDerived(action string) bool { return derivedActions[action] }

func (p *Policy) Evaluate(action, state string) Capabilities {
	c := Capabilities{JumpBack: true, Forget: true, Delete: true}
	switch {
	case derivedActions[action]:
		c.Derived = true
		c.Reason = "follows the change that caused it"
	case action == "account.created" || action == "account.updated":
		c.Revert = true
	case strings.HasPrefix(action, "account."):
		c.Reason = "manage it from Settings → Accounts"
	case strings.HasPrefix(action, "valuation."),
		strings.HasPrefix(action, "category."),
		strings.HasPrefix(action, "transaction."),
		action == "transfer.linked" || action == "transfer.unlink",
		strings.HasPrefix(action, "group_rule."),
		action == "categorisation.applied":
		c.Revert = true
	case action == "import.confirmed":
		c.Revert, c.Forget, c.Delete = true, false, false
		c.Reason = "undo the import first"
	default:
		// Records (setup, schemas, widgets, settings, history bookkeeping)
		// have nothing to replay.
		c.Reason = "record-only entry"
	}
	if state == "inactive" {
		c.Revert = false
		c.Reason = "already undone"
	}
	return c
}
