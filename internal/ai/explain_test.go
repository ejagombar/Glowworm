package ai

import (
	"errors"
	"testing"
)

func TestExplain(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		summary string
		detail  string
	}{
		{"anthropic bad key", &ErrUnavailable{Status: 401, Body: `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`},
			"API key not accepted", "Invalid x-api-key"},
		{"openai bad key", &ErrUnavailable{Status: 401, Body: `{"error":{"message":"Incorrect API key provided: sk-abc. You can find your API key at https://platform.openai.com/account/api-keys.","type":"invalid_request_error"}}`},
			"API key not accepted", "Incorrect API key provided: sk-abc."},
		{"google bad key is a 400", &ErrUnavailable{Status: 400, Body: `{"error":{"code":400,"message":"API key not valid. Please pass a valid API key.","status":"INVALID_ARGUMENT"}}`},
			"API key not accepted", "API key not valid. Please pass a valid API key."},
		{"anthropic credit", &ErrUnavailable{Status: 400, Body: `{"type":"error","error":{"type":"invalid_request_error","message":"Your credit balance is too low to access the Anthropic API."}}`},
			"Out of credit", "Your credit balance is too low to access the Anthropic API."},
		{"model missing", &ErrUnavailable{Status: 404, Body: `{"error":{"message":"The model gpt-9 does not exist"}}`},
			"Model or endpoint not found", "The model gpt-9 does not exist"},
		{"ollama", &ErrUnavailable{Status: 404, Body: `{"error":"model \"llama9\" not found, try pulling it first"}`},
			"Model or endpoint not found", `Model "llama9" not found, try pulling it first`},
		{"html page", &ErrUnavailable{Status: 502, Body: `<html><body>Bad gateway</body></html>`},
			"The provider is having problems", ""},
		{"dns", &ErrUnavailable{Reason: `Post "https://x.invalid/v1": dial tcp: lookup x.invalid: no such host`},
			"Couldn't find the server", "Check the endpoint URL, and that this server can reach the internet."},
		{"no key", &ErrUnavailable{Reason: "no API key configured"}, "No API key saved", ""},
		{"bad reply", &ErrBadReply{Reason: "no JSON in reply"}, "The model answered, but not in a form Glowworm could read", "No JSON in reply"},
		{"other", errors.New("boom"), "Something went wrong", "Boom"},
	}
	for _, c := range cases {
		got := Explain(c.err)
		if got.Summary != c.summary || got.Detail != c.detail {
			t.Errorf("%s: got %q / %q, want %q / %q", c.name, got.Summary, got.Detail, c.summary, c.detail)
		}
	}
}
