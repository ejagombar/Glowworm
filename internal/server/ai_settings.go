package server

import (
	"database/sql"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"glowworm/internal/ai"
	"glowworm/internal/core/audit"
	"glowworm/internal/crypto"
	"glowworm/internal/store"
	"glowworm/internal/store/repos"
)

// Models are chosen per role. Each role's model is stored under the role's
// name and the provider it comes from under "<role>_provider".
const (
	modelRoleCategorisation = "categorisation"
	modelRoleQueries        = "queries"
)

var modelRoles = []string{modelRoleQueries, modelRoleCategorisation}

// aiSetup is the saved AI configuration: the connected providers and the
// model chosen for each role.
type aiSetup struct {
	keys   map[string]*repos.AIProviderKey
	models map[string]string
}

func loadAISetup(r *repos.AIConfig) (*aiSetup, error) {
	row, err := r.Get()
	if err != nil {
		return nil, err
	}
	keys, err := r.Keys()
	if err != nil {
		return nil, err
	}
	return &aiSetup{keys: keys, models: row.Models}, nil
}

// connected lists the connected providers in menu order.
func (s *aiSetup) connected() []string {
	var out []string
	for _, p := range ai.Providers {
		if _, ok := s.keys[p.ID]; ok {
			out = append(out, p.ID)
		}
	}
	return out
}

// forRole says which provider and model a role uses. A role whose provider
// was disconnected (or never chosen) falls back to the first connected
// provider and its default model.
func (s *aiSetup) forRole(role string) (provider, model string) {
	p := s.models[role+"_provider"]
	if _, ok := s.keys[p]; ok && p != "" {
		return p, s.models[role]
	}
	if c := s.connected(); len(c) > 0 {
		return c[0], ""
	}
	return "", ""
}

// pickerValue is a role's model as the settings page's pickers hold it:
// "provider:model", or "" when it's left to the provider's default.
func (s *aiSetup) pickerValue(role string) string {
	p, m := s.forRole(role)
	if m == "" {
		return ""
	}
	return p + ":" + m
}

func (s *aiSetup) client(secret *crypto.Secret, provider, model string) (ai.Provider, error) {
	k := s.keys[provider]
	if k == nil {
		return nil, nil
	}
	return ai.New(provider, model, k.BaseURL, decodeKey(secret, k.KeyCipher))
}

func (s *aiSetup) roleClient(secret *crypto.Secret, role string) (ai.Provider, error) {
	p, m := s.forRole(role)
	return s.client(secret, p, m)
}

// resolveProviders returns the provider for import/categorisation work and
// the one for the Queries page.
func resolveProviders(db *store.DB, secret *crypto.Secret, log *slog.Logger) (cat, queries ai.Provider) {
	if os.Getenv("GLOWWORM_AI_MOCK") == "1" {
		log.Info("using mock AI provider (GLOWWORM_AI_MOCK=1)")
		return &ai.Mock{}, &ai.Mock{}
	}
	s, err := loadAISetup(&repos.AIConfig{DB: db})
	if err != nil {
		return nil, nil
	}
	cat, err = s.roleClient(secret, modelRoleCategorisation)
	if err != nil {
		log.Error("ai provider", "role", modelRoleCategorisation, "err", err)
		cat = nil
	}
	queries, err = s.roleClient(secret, modelRoleQueries)
	if err != nil {
		log.Error("ai provider", "role", modelRoleQueries, "err", err)
		queries = nil
	}
	return cat, queries
}

func decodeKey(secret *crypto.Secret, cipher string) string {
	if cipher == "" {
		return ""
	}
	plain, err := secret.Decrypt(cipher)
	if err != nil {
		return ""
	}
	return string(plain)
}

// aiProviderView is a provider as the settings page shows it.
type aiProviderView struct {
	ai.ProviderSpec
	Connected bool   `json:"connected"`
	HasKey    bool   `json:"has_key"`
	BaseURL   string `json:"base_url"`
}

func (s *aiSetup) views() []aiProviderView {
	out := make([]aiProviderView, 0, len(ai.Providers))
	for _, p := range ai.Providers {
		v := aiProviderView{ProviderSpec: p}
		if k := s.keys[p.ID]; k != nil {
			v.Connected, v.HasKey, v.BaseURL = true, k.KeyCipher != "", k.BaseURL
		}
		out = append(out, v)
	}
	return out
}

func providerShort(id string) string {
	if p, ok := ai.Spec(id); ok {
		return p.Short
	}
	return id
}

func (a *App) apiAIGet(w http.ResponseWriter, r *http.Request) error {
	s, err := loadAISetup(a.Repos.AIConfig)
	if err != nil {
		return err
	}
	writeJSON(w, map[string]any{"providers": s.views(), "models": s.models})
	return nil
}

// aiKeyInput is what the settings page sends for one provider. Nil fields
// leave what's saved alone; Clear disconnects the provider.
type aiKeyInput struct {
	APIKey  string  `json:"api_key"`
	BaseURL *string `json:"base_url"`
	Clear   bool    `json:"clear"`
}

func (a *App) apiAISet(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Keys   map[string]aiKeyInput `json:"keys"`
		Models map[string]string     `json:"models"`
	}
	if err := decodeBody(r, &in); err != nil {
		return err
	}
	before, err := loadAISetup(a.Repos.AIConfig)
	if err != nil {
		return err
	}
	type change struct {
		provider, cipher, baseURL string
		clear                     bool
	}
	var changes []change
	ids := make([]string, 0, len(in.Keys))
	for id := range in.Keys {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		k := in.Keys[id]
		spec, ok := ai.Spec(id)
		if !ok {
			apiError(w, http.StatusBadRequest, "validation_failed", "Unknown provider "+id)
			return nil
		}
		if k.Clear {
			changes = append(changes, change{provider: id, clear: true})
			continue
		}
		c := change{provider: id}
		if old := before.keys[id]; old != nil {
			c.cipher, c.baseURL = old.KeyCipher, old.BaseURL
		}
		if k.BaseURL != nil {
			c.baseURL = strings.TrimSpace(*k.BaseURL)
		}
		if key := strings.TrimSpace(k.APIKey); key != "" {
			if c.cipher, err = a.Secret.Encrypt([]byte(key)); err != nil {
				return err
			}
		}
		if spec.URL == "required" && c.baseURL == "" {
			apiError(w, http.StatusBadRequest, "validation_failed", spec.Short+" needs its endpoint URL")
			return nil
		}
		// A provider that needs a key isn't connected until it has one.
		if spec.NeedsKey && c.cipher == "" {
			continue
		}
		changes = append(changes, c)
	}
	models := before.models
	if in.Models != nil {
		models = in.Models
	}
	if err := a.DB.WithTx(func(tx *sql.Tx) error {
		for _, c := range changes {
			var err error
			if c.clear {
				err = a.Repos.AIConfig.DeleteKey(tx, c.provider)
			} else {
				err = a.Repos.AIConfig.SetKey(tx, c.provider, c.cipher, c.baseURL)
			}
			if err != nil {
				return err
			}
		}
		return a.Repos.AIConfig.SetModels(tx, models)
	}); err != nil {
		return err
	}
	after, err := loadAISetup(a.Repos.AIConfig)
	if err != nil {
		return err
	}
	names := make([]string, 0)
	for _, id := range after.connected() {
		names = append(names, providerShort(id))
	}
	sub := "AI off"
	if len(names) > 0 {
		sub = strings.Join(names, ", ")
	}
	if err := a.DB.WithTx(func(tx *sql.Tx) error {
		_, err := a.AuditWriter.Write(tx, audit.Input{
			Actor: "user", Action: "settings.ai.updated",
			EntityType: "ai_provider_config", EntityID: "1",
			Title:  "Updated AI providers",
			Sub:    sub,
			After:  map[string]any{"providers": after.connected(), "models": models},
			Revert: map[string]any{"providers": before.connected(), "models": before.models},
		})
		return err
	}); err != nil {
		return err
	}
	cat, _ := after.roleClient(a.Secret, modelRoleCategorisation)
	queries, _ := after.roleClient(a.Secret, modelRoleQueries)
	a.setProviders(cat, queries)
	writeJSON(w, map[string]any{"ok": true, "providers": after.views(), "models": after.models})
	return nil
}

// aiModel is one model in the pickers, marked with the provider it's from.
type aiModel struct {
	ai.ModelInfo
	Provider string `json:"provider"`
}

// aiModelSource says how one provider's list was got: "live" from the
// provider, "fallback" (a built-in list, because it couldn't be reached) or
// "needs-key".
type aiModelSource struct {
	Provider string        `json:"provider"`
	Label    string        `json:"label"`
	Source   string        `json:"source"`
	Error    *ai.Explained `json:"error,omitempty"`
}

// apiAIModels lists the models of every connected provider, plus any the
// page has a key or URL typed in for but not yet saved.
func (a *App) apiAIModels(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Keys map[string]struct {
			APIKey  string `json:"api_key"`
			BaseURL string `json:"base_url"`
		} `json:"keys"`
	}
	if err := decodeBody(r, &in); err != nil {
		return err
	}
	s, err := loadAISetup(a.Repos.AIConfig)
	if err != nil {
		return err
	}
	type target struct{ id, key, baseURL string }
	var targets []target
	for _, p := range ai.Providers {
		t := target{id: p.ID}
		saved := s.keys[p.ID]
		if saved != nil {
			t.key, t.baseURL = decodeKey(a.Secret, saved.KeyCipher), saved.BaseURL
		}
		draft, typed := in.Keys[p.ID]
		if typed {
			if k := strings.TrimSpace(draft.APIKey); k != "" {
				t.key = k
			}
			if u := strings.TrimSpace(draft.BaseURL); u != "" {
				t.baseURL = u
			}
		}
		if saved == nil && (!typed || (t.key == "" && t.baseURL == "")) {
			continue
		}
		targets = append(targets, t)
	}
	sources := make([]aiModelSource, len(targets))
	lists := make([][]ai.ModelInfo, len(targets))
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			spec, _ := ai.Spec(t.id)
			src := aiModelSource{Provider: t.id, Label: spec.Short, Source: "live"}
			if spec.NeedsKey && t.key == "" {
				src.Source, lists[i] = "needs-key", ai.FallbackModels(t.id)
				sources[i] = src
				return
			}
			models, err := ai.ListModels(t.id, t.baseURL, t.key, 10*time.Second)
			if err != nil || len(models) == 0 {
				if err == nil {
					err = &ai.ErrUnavailable{Reason: "provider returned no models"}
				}
				e := ai.Explain(err)
				src.Source, src.Error, models = "fallback", &e, ai.FallbackModels(t.id)
			}
			lists[i], sources[i] = models, src
		}()
	}
	wg.Wait()
	out := []aiModel{}
	for i, t := range targets {
		for _, m := range lists[i] {
			out = append(out, aiModel{ModelInfo: m, Provider: t.id})
		}
	}
	writeJSON(w, map[string]any{"ok": true, "models": out, "sources": sources})
	return nil
}

// aiTestResult is how one provider got on in the connection test.
type aiTestResult struct {
	Provider  string        `json:"provider"`
	Label     string        `json:"label"`
	Model     string        `json:"model"`
	OK        bool          `json:"ok"`
	LatencyMS int           `json:"latency_ms,omitempty"`
	Error     *ai.Explained `json:"error,omitempty"`
}

// apiAITest asks every connected provider for a small reply, each with the
// models the roles use from it (or its default when no role does), and says
// how each one got on.
func (a *App) apiAITest(w http.ResponseWriter, r *http.Request) error {
	s, err := loadAISetup(a.Repos.AIConfig)
	if err != nil {
		return err
	}
	type job struct{ provider, model string }
	var jobs []job
	for _, p := range s.connected() {
		seen := map[string]bool{}
		for _, role := range modelRoles {
			if rp, m := s.forRole(role); rp == p && !seen[m] {
				seen[m] = true
				jobs = append(jobs, job{p, m})
			}
		}
		if len(seen) == 0 {
			jobs = append(jobs, job{p, ""})
		}
	}
	results := make([]aiTestResult, len(jobs))
	var wg sync.WaitGroup
	for i, j := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res := aiTestResult{Provider: j.provider, Label: providerShort(j.provider), Model: j.model}
			defer func() { results[i] = res }()
			p, err := s.client(a.Secret, j.provider, j.model)
			if err == nil && p == nil {
				err = &ai.ErrUnavailable{Reason: "unknown provider " + j.provider}
			}
			if err != nil {
				e := ai.Explain(err)
				res.Error = &e
				return
			}
			if res.Model == "" {
				res.Model = ai.EffectiveModel(p)
			}
			start := time.Now()
			_, _, err = p.CompleteStructured(&ai.StructuredRequest{
				Capability: "schema_naming",
				PromptJSON: map[string]any{"columns": []string{"Date", "Description", "Amount"}, "bank_hint": "Test"},
				Timeout:    30 * time.Second,
			})
			res.LatencyMS = int(time.Since(start).Milliseconds())
			if err != nil {
				e := ai.Explain(err)
				res.Error = &e
				a.Repos.AIConfig.LogCall("schema_naming", j.provider, j.model, 0, 0, res.LatencyMS, false, err.Error())
				return
			}
			res.OK = true
			a.Repos.AIConfig.LogCall("schema_naming", j.provider, j.model, 0, 0, res.LatencyMS, true, "")
		}()
	}
	wg.Wait()
	ok := len(results) > 0
	for _, res := range results {
		ok = ok && res.OK
	}
	writeJSON(w, map[string]any{"ok": ok, "results": results})
	return nil
}
