package audit

import "strings"

type Policy struct{}

func (p *Policy) Evaluate(action, state string) Capabilities {
	c := Capabilities{JumpBack: true}
	switch {
	case strings.HasPrefix(action, "account."):
		c.Revert = action == "account.created" || action == "account.updated"
		c.Forget = true
		c.Delete = action != "account.deleted" || true
	case strings.HasPrefix(action, "valuation."):
		c.Revert, c.Forget, c.Delete = true, true, true
	case strings.HasPrefix(action, "category."):
		c.Revert, c.Forget, c.Delete = true, true, true
	case strings.HasPrefix(action, "transaction."):
		c.Revert, c.Forget, c.Delete = true, true, true
	case action == "transfer.linked" || action == "transfer.unlink":
		c.Revert, c.Forget, c.Delete = true, true, true
	case action == "recurring.detected":
		c.Revert, c.Forget, c.Delete = true, true, true
	case action == "import.schema_detected":
		c.Revert, c.Forget, c.Delete = false, true, true
		c.Reason = "record-only entry"
	case action == "import.confirmed":
		c.Revert, c.JumpBack, c.Forget, c.Delete = true, true, false, false
		c.Reason = "use revert (undo the import) first"
	case strings.HasPrefix(action, "schema."),
		strings.HasPrefix(action, "widget."),
		action == "settings.ai.updated":
		c.Revert, c.Forget, c.Delete = true, true, true
	case action == "categorisation.applied":
		c.Revert, c.Forget, c.Delete = true, true, true
	case action == "classifier.retrained":
		c.Revert, c.Forget, c.Delete = true, true, true
	case strings.HasPrefix(action, "audit."):
		c.Revert, c.JumpBack, c.Forget, c.Delete = false, true, true, true
	case strings.HasPrefix(action, "setup."):
		c.Revert, c.Forget, c.Delete = false, true, true
		c.Reason = "record-only entry"
	default:
		c.Revert, c.Forget, c.Delete = true, true, true
	}
	if state == "inactive" {
		c.Revert = false
		if c.Reason == "" {
			c.Reason = "already inactive — restore the path instead"
		} else {
			c.Reason = "already inactive — " + c.Reason
		}
	}
	return c
}
