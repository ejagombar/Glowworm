package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// --- OpenAI-compatible (also serves the "custom" endpoint and OpenRouter,
// doc 09 §3) ---

type OpenAI struct {
	BaseURL      string // default https://api.openai.com/v1
	APIKey       string
	Model        string
	ExtraHeaders map[string]string // e.g. OpenRouter attribution headers
}

func (p *OpenAI) Name() string { return "openai" }

func (p *OpenAI) headers() map[string]string {
	h := map[string]string{
		"Authorization": "Bearer " + p.APIKey,
	}
	for k, v := range p.ExtraHeaders {
		h[k] = v
	}
	return h
}

func (p *OpenAI) CompleteStructured(req *StructuredRequest) (string, *Usage, error) {
	if p.APIKey == "" {
		return "", nil, &ErrUnavailable{Reason: "no API key configured"}
	}
	base := p.BaseURL
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	base = strings.TrimRight(base, "/") // "…/v1/" + "/chat/completions" would 404
	model := p.Model
	if model == "" {
		model = "gpt-4o-mini"
	}
	timeout := req.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	sys, _ := turnsFor(req, "")
	if req.MaxTokens == 0 {
		req.MaxTokens = 4000
	}
	return retry(func(feedback string) (string, *Usage, error) {
		_, turns := turnsFor(req, feedback)
		msgs := make([]map[string]string, 0, len(turns)+1)
		if sys != "" {
			msgs = append(msgs, map[string]string{"role": "system", "content": sys})
		}
		for _, m := range turns {
			msgs = append(msgs, map[string]string{"role": m.Role, "content": m.Content})
		}
		body := map[string]any{
			"model":           model,
			"messages":        msgs,
			"temperature":     0,
			"max_tokens":      req.MaxTokens,
			"response_format": map[string]string{"type": "json_object"},
		}
		var resp struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
			Usage struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
		}
		if err := postJSON(ctx, base+"/chat/completions", p.headers(), body, &resp); err != nil {
			return "", nil, err
		}
		if len(resp.Choices) == 0 {
			return "", nil, &ErrUnavailable{Reason: "empty completion"}
		}
		out, err := extractJSON(resp.Choices[0].Message.Content)
		if err != nil {
			return "", nil, err
		}
		return out, &Usage{PromptTokens: resp.Usage.PromptTokens, CompletionTokens: resp.Usage.CompletionTokens, Model: model}, nil
	}, validJSON)
}

func validJSON(s string) error {
	var v any
	return json.Unmarshal([]byte(s), &v)
}

// --- Anthropic ---

type Anthropic struct {
	APIKey  string
	Model   string
	BaseURL string // default https://api.anthropic.com
}

func (p *Anthropic) Name() string { return "anthropic" }

func (p *Anthropic) CompleteStructured(req *StructuredRequest) (string, *Usage, error) {
	if p.APIKey == "" {
		return "", nil, &ErrUnavailable{Reason: "no API key configured"}
	}
	model := p.Model
	if model == "" {
		model = "claude-3-5-haiku-latest"
	}
	timeout := req.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	sys, _ := turnsFor(req, "")
	if req.MaxTokens == 0 {
		req.MaxTokens = 4000
	}
	return retry(func(feedback string) (string, *Usage, error) {
		_, turns := turnsFor(req, feedback)
		msgs := make([]map[string]string, 0, len(turns))
		for _, m := range turns {
			msgs = append(msgs, map[string]string{"role": m.Role, "content": m.Content})
		}
		body := map[string]any{
			"model":       model,
			"max_tokens":  req.MaxTokens,
			"temperature": 0,
			"messages":    msgs,
		}
		if sys != "" {
			body["system"] = sys
		}
		var resp struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			Usage struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		}
		base := p.BaseURL
		if base == "" {
			base = "https://api.anthropic.com"
		}
		if err := postJSON(ctx, strings.TrimRight(base, "/")+"/v1/messages", map[string]string{
			"x-api-key":         p.APIKey,
			"anthropic-version": "2023-06-01",
		}, body, &resp); err != nil {
			return "", nil, err
		}
		var text strings.Builder
		for _, c := range resp.Content {
			if c.Type == "text" {
				text.WriteString(c.Text)
			}
		}
		out, err := extractJSON(text.String())
		if err != nil {
			return "", nil, err
		}
		return out, &Usage{PromptTokens: resp.Usage.InputTokens, CompletionTokens: resp.Usage.OutputTokens, Model: model}, nil
	}, validJSON)
}

// --- Google Gemini ---

type Gemini struct {
	APIKey  string
	Model   string
	BaseURL string // default https://generativelanguage.googleapis.com
}

func (p *Gemini) Name() string { return "google" }

func (p *Gemini) CompleteStructured(req *StructuredRequest) (string, *Usage, error) {
	if p.APIKey == "" {
		return "", nil, &ErrUnavailable{Reason: "no API key configured"}
	}
	model := p.Model
	if model == "" {
		model = "gemini-2.0-flash"
	}
	timeout := req.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	sys, _ := turnsFor(req, "")
	if req.MaxTokens == 0 {
		req.MaxTokens = 4000
	}
	return retry(func(feedback string) (string, *Usage, error) {
		_, turns := turnsFor(req, feedback)
		contents := make([]map[string]any, 0, len(turns))
		for _, m := range turns {
			role := "user"
			if m.Role == "assistant" {
				role = "model"
			}
			contents = append(contents, map[string]any{"role": role,
				"parts": []map[string]string{{"text": m.Content}}})
		}
		body := map[string]any{
			"contents": contents,
			"generationConfig": map[string]any{
				"temperature":      0,
				"maxOutputTokens":  req.MaxTokens,
				"responseMimeType": "application/json",
			},
		}
		if sys != "" {
			body["systemInstruction"] = map[string]any{
				"parts": []map[string]string{{"text": sys}},
			}
		}
		var resp struct {
			Candidates []struct {
				Content struct {
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"content"`
			} `json:"candidates"`
			UsageMetadata struct {
				PromptTokenCount     int `json:"promptTokenCount"`
				CandidatesTokenCount int `json:"candidatesTokenCount"`
			} `json:"usageMetadata"`
		}
		base := p.BaseURL
		if base == "" {
			base = "https://generativelanguage.googleapis.com"
		}
		url := fmt.Sprintf("%s/v1beta/models/%s:generateContent?key=%s", strings.TrimRight(base, "/"), model, p.APIKey)
		if err := postJSON(ctx, url, nil, body, &resp); err != nil {
			return "", nil, err
		}
		if len(resp.Candidates) == 0 {
			return "", nil, &ErrUnavailable{Reason: "empty completion"}
		}
		var text strings.Builder
		for _, part := range resp.Candidates[0].Content.Parts {
			text.WriteString(part.Text)
		}
		out, err := extractJSON(text.String())
		if err != nil {
			return "", nil, err
		}
		return out, &Usage{PromptTokens: resp.UsageMetadata.PromptTokenCount, CompletionTokens: resp.UsageMetadata.CandidatesTokenCount, Model: model}, nil
	}, validJSON)
}

// --- Ollama (local, doc 09 §3) ---

type Ollama struct {
	BaseURL string // default http://localhost:11434
	Model   string
}

func (p *Ollama) Name() string { return "ollama" }

func (p *Ollama) CompleteStructured(req *StructuredRequest) (string, *Usage, error) {
	base := p.BaseURL
	if base == "" {
		base = "http://localhost:11434"
	}
	base = strings.TrimRight(base, "/")
	model := p.Model
	if model == "" {
		model = "llama3.1"
	}
	timeout := req.Timeout
	if timeout == 0 {
		timeout = 120 * time.Second // local models are slower
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	sys, _ := turnsFor(req, "")
	return retry(func(feedback string) (string, *Usage, error) {
		_, turns := turnsFor(req, feedback)
		prompt := ""
		if sys != "" {
			prompt = sys + "\n\n"
		}
		for _, m := range turns {
			role := "Human"
			if m.Role == "assistant" {
				role = "Assistant"
			}
			prompt += role + ": " + m.Content + "\n\n"
		}
		prompt += "Assistant:"
		body := map[string]any{
			"model":   model,
			"prompt":  prompt,
			"stream":  false,
			"format":  "json",
			"options": map[string]any{"temperature": 0},
		}
		var resp struct {
			Response string `json:"response"`
		}
		if err := postJSON(ctx, base+"/api/generate", nil, body, &resp); err != nil {
			return "", nil, err
		}
		out, err := extractJSON(resp.Response)
		if err != nil {
			return "", nil, err
		}
		return out, &Usage{Model: model}, nil
	}, validJSON)
}

// EffectiveModel reports the model a provider will actually call — the
// configured one, or the per-provider default that New fills in for "Auto".
// Useful in error messages so 404s point at a concrete model ID.
func EffectiveModel(p Provider) string {
	switch v := p.(type) {
	case *OpenAI:
		return v.Model
	case *Anthropic:
		return v.Model
	case *Gemini:
		return v.Model
	case *Ollama:
		return v.Model
	default:
		return ""
	}
}

// New builds a provider from config (doc 09 §4).
func New(provider, model, baseURL, apiKey string) (Provider, error) {
	switch provider {
	case "openai":
		return &OpenAI{APIKey: apiKey, Model: model, BaseURL: baseURL}, nil
	case "openrouter":
		// OpenRouter speaks the OpenAI chat-completions dialect; its model IDs
		// are "vendor/model". Attribution headers are recommended by OpenRouter.
		base := baseURL
		if base == "" {
			base = "https://openrouter.ai/api/v1"
		}
		if model == "" {
			model = "openai/gpt-4o-mini" // "Auto": the OpenAI-only default would 404 here
		}
		return &OpenAI{
			APIKey: apiKey, Model: model, BaseURL: base,
			ExtraHeaders: map[string]string{
				"HTTP-Referer": "https://glowworm.app",
				"X-Title":      "Glowworm",
			},
		}, nil
	case "anthropic":
		return &Anthropic{APIKey: apiKey, Model: model}, nil
	case "google":
		return &Gemini{APIKey: apiKey, Model: model}, nil
	case "ollama":
		return &Ollama{BaseURL: baseURL, Model: model}, nil
	case "custom":
		if baseURL == "" {
			return nil, &ErrUnavailable{Reason: "custom provider needs a base URL"}
		}
		return &OpenAI{APIKey: apiKey, Model: model, BaseURL: baseURL}, nil
	case "":
		return nil, nil
	default:
		return nil, &ErrUnavailable{Reason: "unknown provider " + provider}
	}
}
