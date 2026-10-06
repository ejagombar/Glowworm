package ai

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestListModelsOpenRouter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[
			{"id":"openai/gpt-4o-mini","name":"OpenAI: GPT-4o mini"},
			{"id":"anthropic/claude-3.5-sonnet","name":"Anthropic: Claude 3.5 Sonnet"},
			{"id":"","name":"broken entry"}
		]}`))
	}))
	defer srv.Close()
	models, err := ListModels("openrouter", srv.URL+"/api/v1", "sk-or-test", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 {
		t.Fatalf("got %d models, want 2 (empty ids skipped)", len(models))
	}
	if models[0].ID != "openai/gpt-4o-mini" || models[0].Name != "OpenAI: GPT-4o mini" {
		t.Errorf("model[0] = %+v", models[0])
	}
}

func TestListModelsOpenAI(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4o-mini"},{"id":"gpt-4o"}]}`))
	}))
	defer srv.Close()
	models, err := ListModels("openai", srv.URL, "sk-test", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer sk-test" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if len(models) != 2 || models[0].ID != "gpt-4o-mini" {
		t.Errorf("models = %+v", models)
	}
}

func TestListModelsUnknownProvider(t *testing.T) {
	if _, err := ListModels("nonsense", "", "", 5*time.Second); err == nil {
		t.Fatal("unknown provider should error")
	}
}

func TestListModelsOpenAIWithoutKey(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"data":[{"id":"m1"}]}`))
	}))
	defer srv.Close()
	models, err := ListModels("openai", srv.URL, "", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "" {
		t.Errorf("unexpected Authorization header %q", gotAuth)
	}
	if len(models) != 1 {
		t.Errorf("models = %+v", models)
	}
}

func TestListModelsOllama(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"models":[{"name":"llama3.2:latest"},{"name":"qwen2.5:7b"}]}`))
	}))
	defer srv.Close()
	models, err := ListModels("ollama", srv.URL, "", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[1].ID != "qwen2.5:7b" {
		t.Errorf("models = %+v", models)
	}
}

func TestListModelsCustomRequiresBase(t *testing.T) {
	_, err := ListModels("custom", "", "k", 5*time.Second)
	if err == nil {
		t.Fatal("custom without base URL should error")
	}
}

func TestFallbackModels(t *testing.T) {
	for _, p := range []string{"openrouter", "openai", "anthropic", "google", "ollama"} {
		if len(FallbackModels(p)) == 0 {
			t.Errorf("no fallback models for %s", p)
		}
	}
	if FallbackModels("custom") != nil {
		t.Error("custom should have no fallback list")
	}
	if FallbackModels("nonsense") != nil {
		t.Error("unknown provider should have no fallback list")
	}
}

func TestOpenRouterProviderRoundTrip(t *testing.T) {
	var gotPath, gotTitle, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotTitle = r.Header.Get("X-Title")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"name\":\"X\"}"}}]}`))
	}))
	defer srv.Close()
	p, err := New("openrouter", "openai/gpt-4o-mini", srv.URL, "sk-or-test")
	if err != nil {
		t.Fatal(err)
	}
	out, _, err := p.CompleteStructured(&StructuredRequest{Capability: "schema_naming", PromptJSON: map[string]any{"columns": []string{"Date"}}})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/chat/completions" {
		t.Errorf("path = %q", gotPath)
	}
	if gotTitle != "Glowworm" {
		t.Errorf("X-Title = %q", gotTitle)
	}
	if out != `{"name":"X"}` {
		t.Errorf("out = %q", out)
	}
	if !strings.Contains(gotBody, "openai/gpt-4o-mini") {
		t.Errorf("request body missing model: %q", gotBody)
	}
	p2, err := New("openrouter", "", "", "k")
	if err != nil {
		t.Fatal(err)
	}
	o, ok := p2.(*OpenAI)
	if !ok {
		t.Fatal("openrouter should be an *OpenAI")
	}
	if o.BaseURL != "https://openrouter.ai/api/v1" {
		t.Errorf("default base = %q", o.BaseURL)
	}
	if o.Model != "openai/gpt-4o-mini" {
		t.Errorf("default model = %q, want openai/gpt-4o-mini", o.Model)
	}
	p3, err := New("openrouter", "openai/gpt-4o-mini", srv.URL+"/", "k")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := p3.CompleteStructured(&StructuredRequest{Capability: "schema_naming", PromptJSON: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/chat/completions" {
		t.Errorf("path with trailing-slash base = %q, want /chat/completions", gotPath)
	}
}
