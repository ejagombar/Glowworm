package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/ejagombar/Glowworm/internal/store/repos"
)

// Two providers connect side by side, each role keeps the provider its model
// is from, and disconnecting one sends its role back to the other.
func TestAISettingsSeveralProviders(t *testing.T) {
	c := &testClient{t: t, h: newTestApp(t), cookies: map[string]*http.Cookie{}}
	c.login("longpassword1")

	type view struct {
		ID        string `json:"id"`
		Connected bool   `json:"connected"`
		HasKey    bool   `json:"has_key"`
	}
	type state struct {
		Providers []view            `json:"providers"`
		Models    map[string]string `json:"models"`
	}
	put := func(body string) (int, state) {
		rec := c.do("PUT", "/api/v1/settings/ai", "application/json", body)
		var s state
		_ = json.Unmarshal(rec.Body.Bytes(), &s)
		return rec.Code, s
	}
	connected := func(s state) map[string]bool {
		out := map[string]bool{}
		for _, p := range s.Providers {
			if p.Connected {
				out[p.ID] = p.HasKey
			}
		}
		return out
	}

	code, s := put(`{"keys":{"anthropic":{"api_key":"sk-ant-1"},"google":{"api_key":"g-2"},"openai":{}},
		"models":{"queries":"claude-x","queries_provider":"anthropic","categorisation":"gemini-y","categorisation_provider":"google"}}`)
	if code != 200 {
		t.Fatalf("save: %d", code)
	}
	if got := connected(s); len(got) != 2 || !got["anthropic"] || !got["google"] {
		t.Fatalf("connected = %v, want anthropic and google (openai had no key)", got)
	}

	if code, _ := put(`{"keys":{"custom":{"base_url":""}}}`); code != 400 {
		t.Fatalf("custom without URL: %d, want 400", code)
	}

	// Saving models only leaves the keys alone.
	_, s = put(`{"models":{"queries":"claude-x","queries_provider":"anthropic"}}`)
	if got := connected(s); !got["anthropic"] || !got["google"] {
		t.Fatalf("keys lost on a models-only save: %v", got)
	}

	_, s = put(`{"keys":{"anthropic":{"clear":true}}}`)
	if got := connected(s); got["anthropic"] || !got["google"] {
		t.Fatalf("after disconnect: %v", got)
	}
}

func TestAISetupForRole(t *testing.T) {
	s := &aiSetup{
		keys: map[string]*repos.AIProviderKey{"google": {Provider: "google"}, "openai": {Provider: "openai"}},
		models: map[string]string{
			"queries": "claude-x", "queries_provider": "anthropic", // disconnected
			"categorisation": "gemini-y", "categorisation_provider": "google",
		},
	}
	if p, m := s.forRole(modelRoleCategorisation); p != "google" || m != "gemini-y" {
		t.Errorf("categorisation = %s/%s", p, m)
	}
	// Its provider is gone, so the first connected one in menu order, on its default.
	if p, m := s.forRole(modelRoleQueries); p != "openai" || m != "" {
		t.Errorf("queries = %s/%q, want openai default", p, m)
	}
	if v := s.pickerValue(modelRoleCategorisation); v != "google:gemini-y" {
		t.Errorf("picker value = %q", v)
	}
}
