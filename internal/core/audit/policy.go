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

// logOnly reports whether an action only records that something happened,
// leaving nothing behind that a rewind could be expected to take back.
func logOnly(action string) bool {
	return strings.HasPrefix(action, "setup.") || strings.HasPrefix(action, "import.")
}

func (p *Policy) Evaluate(action, state string) Capabilities {
	var c Capabilities
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
		action == "change.applied",
		action == "import.confirmed" || action == "import.edited":
		c.Revert = true
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
