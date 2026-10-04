// Models listing (doc 09): fetch the models a provider currently offers so
// the settings UI can offer real choices instead of a hardcoded guess.
package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ModelInfo is one selectable model: ID is what the provider API expects
// (e.g. "openai/gpt-4o-mini"), Name is a human label.
type ModelInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// getJSON fetches url with headers and decodes the JSON body into out.
func getJSON(ctx context.Context, url string, headers map[string]string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return &ErrUnavailable{Reason: err.Error()}
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<22))
	if resp.StatusCode/100 != 2 {
		return &ErrUnavailable{Reason: fmt.Sprintf("%s: %s", resp.Status, truncate(string(data), 300))}
	}
	return json.Unmarshal(data, out)
}

// ListModels fetches the live model list for a provider. base and key may be
// empty for providers that need neither (OpenRouter's model catalogue is
// public). Returns an error when the provider is unknown or the API is
// unreachable — the caller decides whether to fall back to FallbackModels.
func ListModels(provider, baseURL, apiKey string, timeout time.Duration) ([]ModelInfo, error) {
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	base := strings.TrimRight(baseURL, "/")
	switch provider {
	case "openrouter":
		if base == "" {
			base = "https://openrouter.ai/api/v1"
		}
		headers := map[string]string{}
		if apiKey != "" {
			headers["Authorization"] = "Bearer " + apiKey
		}
		var resp struct {
			Data []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"data"`
		}
		if err := getJSON(ctx, base+"/models", headers, &resp); err != nil {
			return nil, err
		}
		out := make([]ModelInfo, 0, len(resp.Data))
		for _, m := range resp.Data {
			if m.ID == "" {
				continue
			}
			out = append(out, ModelInfo{ID: m.ID, Name: m.Name})
		}
		return out, nil

	case "openai", "custom":
		if base == "" {
			if provider == "custom" {
				return nil, &ErrUnavailable{Reason: "custom provider needs a base URL"}
			}
			base = "https://api.openai.com/v1"
		}
		headers := map[string]string{}
		if apiKey != "" {
			headers["Authorization"] = "Bearer " + apiKey
		}
		var resp struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := getJSON(ctx, base+"/models", headers, &resp); err != nil {
			return nil, err
		}
		out := make([]ModelInfo, 0, len(resp.Data))
		for _, m := range resp.Data {
			if m.ID == "" {
				continue
			}
			out = append(out, ModelInfo{ID: m.ID, Name: m.ID})
		}
		return out, nil

	case "anthropic":
		headers := map[string]string{
			"x-api-key":         apiKey,
			"anthropic-version": "2023-06-01",
		}
		var resp struct {
			Data []struct {
				ID          string `json:"id"`
				DisplayName string `json:"display_name"`
			} `json:"data"`
		}
		if err := getJSON(ctx, "https://api.anthropic.com/v1/models", headers, &resp); err != nil {
			return nil, err
		}
		out := make([]ModelInfo, 0, len(resp.Data))
		for _, m := range resp.Data {
			if m.ID == "" {
				continue
			}
			name := m.DisplayName
			if name == "" {
				name = m.ID
			}
			out = append(out, ModelInfo{ID: m.ID, Name: name})
		}
		return out, nil

	case "google":
		var resp struct {
			Models []struct {
				Name        string `json:"name"`
				DisplayName string `json:"displayName"`
			} `json:"models"`
		}
		url := "https://generativelanguage.googleapis.com/v1beta/models"
		if apiKey != "" {
			url += "?key=" + apiKey
		}
		if err := getJSON(ctx, url, nil, &resp); err != nil {
			return nil, err
		}
		out := make([]ModelInfo, 0, len(resp.Models))
		for _, m := range resp.Models {
			id := strings.TrimPrefix(m.Name, "models/")
			if id == "" {
				continue
			}
			name := m.DisplayName
			if name == "" {
				name = id
			}
			out = append(out, ModelInfo{ID: id, Name: name})
		}
		return out, nil

	case "ollama":
		if base == "" {
			base = "http://localhost:11434"
		}
		var resp struct {
			Models []struct {
				Name string `json:"name"`
			} `json:"models"`
		}
		if err := getJSON(ctx, base+"/api/tags", nil, &resp); err != nil {
			return nil, err
		}
		out := make([]ModelInfo, 0, len(resp.Models))
		for _, m := range resp.Models {
			if m.Name == "" {
				continue
			}
			out = append(out, ModelInfo{ID: m.Name, Name: m.Name})
		}
		return out, nil

	default:
		return nil, &ErrUnavailable{Reason: "unknown provider " + provider}
	}
}

// FallbackModels is a small curated list per provider, used when the live
// catalogue can't be fetched (offline, wrong key, provider down) so the UI
// still offers sensible choices.
func FallbackModels(provider string) []ModelInfo {
	list := func(ids ...string) []ModelInfo {
		out := make([]ModelInfo, 0, len(ids))
		for _, id := range ids {
			out = append(out, ModelInfo{ID: id, Name: id})
		}
		return out
	}
	switch provider {
	case "openrouter":
		return list(
			"openai/gpt-4o-mini",
			"openai/gpt-4o",
			"anthropic/claude-3.5-sonnet",
			"google/gemini-2.0-flash-001",
			"meta-llama/llama-3.3-70b-instruct",
			"deepseek/deepseek-chat",
		)
	case "openai":
		return list("gpt-4o-mini", "gpt-4o", "gpt-4.1-mini", "o4-mini")
	case "anthropic":
		return list(
			"claude-3-5-haiku-latest",
			"claude-3-5-sonnet-latest",
			"claude-sonnet-4-0",
			"claude-opus-4-0",
		)
	case "google":
		return list("gemini-2.0-flash", "gemini-2.0-flash-lite", "gemini-1.5-pro", "gemini-1.5-flash")
	case "ollama":
		return list("llama3.2", "qwen2.5", "mistral", "phi4")
	case "custom":
		return nil // no sensible guess for an arbitrary endpoint
	default:
		return nil
	}
}
