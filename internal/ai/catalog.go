package ai

// ProviderSpec describes one provider Glowworm can talk to.
type ProviderSpec struct {
	ID    string `json:"id"`
	Label string `json:"label"` // as listed in the provider menu
	Short string `json:"short"` // the marker beside its models
	// NeedsKey: it can't be used without an API key.
	NeedsKey bool `json:"needs_key"`
	// URL: "" when there's no endpoint to set, "optional" when the default
	// can be overridden, "required" when there is no default.
	URL        string `json:"url"`
	DefaultURL string `json:"default_url,omitempty"`
}

// Providers lists every provider, in the order they're offered. When a role
// has no provider chosen, the first connected one in this order is used.
var Providers = []ProviderSpec{
	{ID: "openai", Label: "OpenAI", Short: "OpenAI", NeedsKey: true},
	{ID: "anthropic", Label: "Anthropic", Short: "Anthropic", NeedsKey: true},
	{ID: "google", Label: "Gemini", Short: "Gemini", NeedsKey: true},
	{ID: "openrouter", Label: "OpenRouter", Short: "OpenRouter", NeedsKey: true, URL: "optional", DefaultURL: "https://openrouter.ai/api/v1"},
	{ID: "ollama", Label: "Ollama", Short: "Ollama", URL: "optional", DefaultURL: "http://localhost:11434"},
	{ID: "custom", Label: "Custom endpoint", Short: "Custom", URL: "required"},
}

// Spec returns the provider with this id.
func Spec(id string) (ProviderSpec, bool) {
	for _, p := range Providers {
		if p.ID == id {
			return p, true
		}
	}
	return ProviderSpec{}, false
}
