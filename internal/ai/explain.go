package ai

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// Explained is a provider error put plainly: Summary says what went wrong in
// a few words, Detail (which may be empty) is the provider's own message.
type Explained struct {
	Summary string `json:"summary"`
	Detail  string `json:"detail,omitempty"`
}

// Explain turns an error from a provider into something to show a person.
// Providers word their errors differently, so it reads the HTTP status and
// whichever message field the body has, and falls back to the raw text.
func Explain(err error) Explained {
	if err == nil {
		return Explained{}
	}
	var bad *ErrBadReply
	if errors.As(err, &bad) {
		return Explained{Summary: "The model answered, but not in a form Glowworm could read", Detail: tidy(strings.TrimPrefix(bad.Reason, "ai: "))}
	}
	var u *ErrUnavailable
	if !errors.As(err, &u) {
		return Explained{Summary: "Something went wrong", Detail: tidy(err.Error())}
	}
	if u.Status != 0 {
		msg := providerMessage(u.Body)
		return Explained{Summary: statusSummary(u.Status, msg), Detail: msg}
	}
	r := strings.ToLower(u.Reason)
	has := func(subs ...string) bool {
		for _, s := range subs {
			if strings.Contains(r, s) {
				return true
			}
		}
		return false
	}
	switch {
	case has("no api key"):
		return Explained{Summary: "No API key saved"}
	case has("needs a base url"):
		return Explained{Summary: "Enter the endpoint URL"}
	case has("unknown provider"):
		return Explained{Summary: "Unknown provider"}
	case has("no such host"):
		return Explained{Summary: "Couldn't find the server", Detail: "Check the endpoint URL, and that this server can reach the internet."}
	case has("connection refused"):
		return Explained{Summary: "Nothing answered at the endpoint", Detail: "Check it's running and the URL is right."}
	case has("deadline exceeded", "timeout", "timed out"):
		return Explained{Summary: "The provider took too long to answer"}
	case has("x509", "certificate", "tls"):
		return Explained{Summary: "Couldn't make a secure connection", Detail: tidy(u.Reason)}
	case has("empty completion", "no models"):
		return Explained{Summary: "The provider sent back nothing"}
	}
	return Explained{Summary: "Couldn't reach the provider", Detail: tidy(u.Reason)}
}

func (e Explained) String() string {
	if e.Detail == "" {
		return e.Summary
	}
	return e.Summary + ": " + e.Detail
}

func statusSummary(status int, msg string) string {
	m := strings.ToLower(msg)
	switch {
	// Google answers a bad key with 400; Anthropic and OpenAI say "credit"
	// and "quota" for an empty balance, under 400 or 429.
	case strings.Contains(m, "api key") && (strings.Contains(m, "not valid") || strings.Contains(m, "invalid") || strings.Contains(m, "incorrect")):
		return "API key not accepted"
	case strings.Contains(m, "credit balance") || strings.Contains(m, "insufficient_quota") || strings.Contains(m, "exceeded your current quota") || strings.Contains(m, "insufficient credits"):
		return "Out of credit"
	}
	switch {
	case status == http.StatusUnauthorized:
		return "API key not accepted"
	case status == http.StatusPaymentRequired:
		return "Out of credit"
	case status == http.StatusForbidden:
		return "This key isn't allowed to do that"
	case status == http.StatusNotFound:
		return "Model or endpoint not found"
	case status == http.StatusRequestTimeout || status == http.StatusGatewayTimeout:
		return "The provider took too long to answer"
	case status == http.StatusTooManyRequests:
		return "Rate limit or quota reached"
	case status == 529 || status == http.StatusServiceUnavailable:
		return "The provider is overloaded, try again shortly"
	case status >= 500:
		return "The provider is having problems"
	case status == http.StatusBadRequest:
		return "The provider refused the request"
	}
	return fmt.Sprintf("The provider returned an error (%d)", status)
}

// providerMessage digs the human part out of an error body. Seen shapes:
//
//	{"error":{"message":"…"}}                  OpenAI, OpenRouter, Google
//	{"type":"error","error":{"type":"…","message":"…"}}  Anthropic
//	{"error":"…"}                              Ollama
//	{"message":"…"} / {"detail":"…"}           assorted proxies
func providerMessage(body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	var v any
	if json.Unmarshal([]byte(body), &v) == nil {
		if s := findMessage(v, 0); s != "" {
			return tidy(s)
		}
		return ""
	}
	if strings.HasPrefix(body, "<") { // an HTML error page says nothing useful
		return ""
	}
	return tidy(body)
}

func findMessage(v any, depth int) string {
	if depth > 3 {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case []any:
		if len(t) > 0 {
			return findMessage(t[0], depth+1)
		}
	case map[string]any:
		for _, k := range []string{"message", "error", "detail", "error_description", "msg"} {
			if x, ok := t[k]; ok {
				if s := findMessage(x, depth+1); s != "" {
					return s
				}
			}
		}
	}
	return ""
}

var spaces = regexp.MustCompile(`\s+`)

// tidy makes a message fit for one or two lines: no "ai:" prefix, single
// spaces, a capital letter, no doc links, and not too long.
func tidy(s string) string {
	s = strings.TrimSpace(spaces.ReplaceAllString(s, " "))
	s = strings.TrimPrefix(s, "ai: ")
	// "… Please pass a valid API key. For more information see https://…"
	if i := strings.Index(s, " For more information"); i > 0 {
		s = s[:i]
	}
	if i := strings.Index(s, " You can find your API key at"); i > 0 {
		s = s[:i]
	}
	s = truncate(strings.TrimSpace(s), 240)
	if s != "" {
		s = strings.ToUpper(s[:1]) + s[1:]
	}
	return s
}
