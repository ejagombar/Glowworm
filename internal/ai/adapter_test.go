package ai

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const transcriptSystem = "SYSTEMPROMPT"
const transcriptUser = "USERQUESTION"
const transcriptAssistant = "ASSISTANTTURN"

func transcriptRequest() *StructuredRequest {
	return &StructuredRequest{
		Capability: "query_agent",
		System:     transcriptSystem,
		Messages: []ChatMessage{
			{Role: "user", Content: transcriptUser},
			{Role: "assistant", Content: transcriptAssistant},
		},
	}
}

func requireTranscriptJSON(t *testing.T, body string, check func(map[string]any)) {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("bad JSON request body: %v\n%s", err, body)
	}
	check(m)
}

func expectContains(t *testing.T, body string, needles ...string) {
	t.Helper()
	for _, n := range needles {
		if !strings.Contains(body, n) {
			t.Errorf("request body missing %q:\n%s", n, body)
		}
	}
}

func serve(t *testing.T, respond func(path string) string, captured *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		*captured = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(respond(r.URL.Path)))
	}))
}

func TestOpenAITranscript(t *testing.T) {
	var captured string
	srv := serve(t, func(_ string) string {
		return `{"choices":[{"message":{"content":"{\"ok\":true}"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`
	}, &captured)
	defer srv.Close()
	p := &OpenAI{BaseURL: srv.URL, APIKey: "k", Model: "m"}
	out, _, err := p.CompleteStructured(transcriptRequest())
	if err != nil {
		t.Fatal(err)
	}
	if out != `{"ok":true}` {
		t.Errorf("out = %q", out)
	}
	expectContains(t, captured, transcriptSystem, transcriptUser, transcriptAssistant)
	requireTranscriptJSON(t, captured, func(m map[string]any) {
		msgs, _ := m["messages"].([]any)
		if len(msgs) != 3 {
			t.Fatalf("messages = %d, want 3 (system + 2 turns)", len(msgs))
		}
		first := msgs[0].(map[string]any)
		if first["role"] != "system" {
			t.Errorf("first message role = %v, want system", first["role"])
		}
	})
}

func TestAnthropicTranscript(t *testing.T) {
	var captured string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		captured = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"{\"ok\":true}"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer srv.Close()
	p := &Anthropic{BaseURL: srv.URL, APIKey: "k", Model: "m"}
	out, _, err := p.CompleteStructured(transcriptRequest())
	if err != nil {
		t.Fatal(err)
	}
	if out != `{"ok":true}` {
		t.Errorf("out = %q", out)
	}
	expectContains(t, captured, transcriptSystem, transcriptUser, transcriptAssistant)
	requireTranscriptJSON(t, captured, func(m map[string]any) {
		if m["system"] != transcriptSystem {
			t.Errorf("system field = %v", m["system"])
		}
		msgs, _ := m["messages"].([]any)
		if len(msgs) != 2 {
			t.Fatalf("messages = %d, want 2", len(msgs))
		}
		if msgs[0].(map[string]any)["role"] != "user" || msgs[1].(map[string]any)["role"] != "assistant" {
			t.Errorf("turn roles wrong: %v", msgs)
		}
	})
}

func TestGeminiTranscript(t *testing.T) {
	var captured string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		captured = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"{\"ok\":true}"}]}}],"usageMetadata":{}}`))
	}))
	defer srv.Close()
	p := &Gemini{BaseURL: srv.URL, APIKey: "k", Model: "m"}
	out, _, err := p.CompleteStructured(transcriptRequest())
	if err != nil {
		t.Fatal(err)
	}
	if out != `{"ok":true}` {
		t.Errorf("out = %q", out)
	}
	expectContains(t, captured, transcriptSystem, transcriptUser, transcriptAssistant)
	requireTranscriptJSON(t, captured, func(m map[string]any) {
		contents, _ := m["contents"].([]any)
		if len(contents) != 2 {
			t.Fatalf("contents = %d, want 2", len(contents))
		}
		if contents[0].(map[string]any)["role"] != "user" || contents[1].(map[string]any)["role"] != "model" {
			t.Errorf("turn roles wrong: %v", contents)
		}
		si, ok := m["systemInstruction"].(map[string]any)
		if !ok {
			t.Fatal("systemInstruction missing")
		}
		parts, _ := si["parts"].([]any)
		if parts[0].(map[string]any)["text"] != transcriptSystem {
			t.Errorf("systemInstruction text = %v", parts[0])
		}
	})
}

func TestOllamaTranscript(t *testing.T) {
	var captured string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		captured = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":"{\"ok\":true}"}`))
	}))
	defer srv.Close()
	p := &Ollama{BaseURL: srv.URL, Model: "m"}
	out, _, err := p.CompleteStructured(transcriptRequest())
	if err != nil {
		t.Fatal(err)
	}
	if out != `{"ok":true}` {
		t.Errorf("out = %q", out)
	}
	sysIdx := strings.Index(captured, transcriptSystem)
	userIdx := strings.Index(captured, "Human: "+transcriptUser)
	asstIdx := strings.Index(captured, "Assistant: "+transcriptAssistant)
	if sysIdx < 0 || userIdx < 0 || asstIdx < 0 {
		t.Fatalf("transcript missing pieces:\n%s", captured)
	}
	if !(sysIdx < userIdx && userIdx < asstIdx) {
		t.Errorf("transcript out of order:\n%s", captured)
	}
}

func TestSingleShotStillUsesCapabilityPrompt(t *testing.T) {
	var captured string
	srv := serve(t, func(_ string) string {
		return `{"choices":[{"message":{"content":"{\"items\":[]}"}}],"usage":{}}`
	}, &captured)
	defer srv.Close()
	p := &OpenAI{BaseURL: srv.URL, APIKey: "k", Model: "m"}
	if _, _, err := p.CompleteStructured(&StructuredRequest{
		Capability: "categorisation",
		PromptJSON: map[string]any{"transactions": []any{}},
		Timeout:    2 * time.Second,
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(captured, transcriptSystem) {
		t.Error("single-shot must not send an agent transcript")
	}
	if !strings.Contains(captured, "Categorise every transaction") {
		t.Errorf("capability prompt missing:\n%s", captured)
	}
}
