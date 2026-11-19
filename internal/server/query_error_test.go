package server

import (
	"strings"
	"testing"

	"github.com/ejagombar/Glowworm/internal/ai"
)

type failingProvider struct{ err error }

func (f failingProvider) Name() string { return "failing" }
func (f failingProvider) CompleteStructured(*ai.StructuredRequest) (string, *ai.Usage, error) {
	return "", nil, f.err
}

// A provider's error reaches the Queries page explained, as in Settings:
// what went wrong, and the provider's message apart, not its raw JSON.
func TestQueryStreamExplainsProviderErrors(t *testing.T) {
	a, c := newLockTestApp(t)
	a.Query.Provider = failingProvider{&ai.ErrUnavailable{Reason: "status 400", Status: 400,
		Body: `{"type":"error","error":{"type":"invalid_request_error","message":"Your credit balance is too low to access the Anthropic API."}}`}}
	body := c.do("POST", "/api/v1/query/stream", "application/json", `{"text":"Where did my money go?"}`).Body.String()
	if !strings.Contains(body, `"message":"The AI provider: Out of credit"`) ||
		!strings.Contains(body, `"detail":"Your credit balance is too low to access the Anthropic API."`) {
		t.Errorf("stream: %s", body)
	}
	if strings.Contains(body, "invalid_request_error") {
		t.Errorf("the raw reply reached the page: %s", body)
	}
}
