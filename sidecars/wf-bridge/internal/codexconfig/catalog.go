package codexconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// A content-addressed copy preserves the user's original catalog. Restoring a
// config backup restores its original pointer; no catalog file is overwritten.
type catalogPlan struct {
	path    string
	content []byte
}

func parseModelCatalog(data []byte) (map[string]any, []any, error) {
	var root map[string]any
	if json.Unmarshal(data, &root) != nil {
		return nil, nil, errors.New("Codex model catalog is not valid JSON")
	}
	models, ok := root["models"].([]any)
	if !ok {
		return nil, nil, errors.New("Codex model catalog must contain a models array")
	}
	for _, raw := range models {
		model, ok := raw.(map[string]any)
		if !ok || strings.TrimSpace(stringValue(model["slug"])) == "" {
			return nil, nil, errors.New("Codex model catalog entry is missing its slug")
		}
	}
	return root, models, nil
}

func catalogPath(home, configured string) string {
	if filepath.IsAbs(configured) {
		return configured
	}
	return filepath.Join(home, configured)
}

func fallbackModel(id string, window int64) map[string]any {
	return map[string]any{
		"slug": id, "display_name": id, "description": id,
		"default_reasoning_level": "medium",
		"supported_reasoning_levels": []any{
			map[string]any{"effort": "low", "description": "Low"},
			map[string]any{"effort": "medium", "description": "Medium"},
			map[string]any{"effort": "high", "description": "High"},
		},
		"shell_type": "shell_command", "visibility": "list", "supported_in_api": true,
		"priority": 100, "availability_nux": nil, "upgrade": nil,
		"base_instructions": "", "supports_reasoning_summaries": false,
		"support_verbosity": false, "default_verbosity": nil,
		"apply_patch_tool_type": "freeform", "context_window": window,
		"truncation_policy":            map[string]any{"mode": "tokens", "limit": 10000},
		"supports_parallel_tool_calls": false, "input_modalities": []string{"text", "image"},
		"experimental_supported_tools": []any{},
	}
}

func (m *Manager) prepareModelCatalog(original []byte, input ApplyConfig) (catalogPlan, error) {
	var config map[string]any
	if err := toml.Unmarshal(original, &config); err != nil {
		return catalogPlan{}, errors.New("invalid Codex config")
	}
	// The native profile workflow handles profile overlays transactionally. The
	// legacy single-file helper must not silently claim success behind an overlay.
	if name := strings.TrimSpace(stringValue(config["profile"])); name != "" {
		profiles, _ := mapValue(config["profiles"])
		selected, _ := mapValue(profiles[name])
		_, overrides := selected["model_catalog_json"]
		if strings.ContainsAny(name, "/\\") {
			return catalogPlan{}, errors.New("invalid Codex profile name")
		}
		if data, err := os.ReadFile(filepath.Join(m.CodexHome, name+".config.toml")); err == nil {
			var overlay map[string]any
			if toml.Unmarshal(data, &overlay) != nil {
				return catalogPlan{}, errors.New("invalid selected Codex profile")
			}
			_, fileOverride := overlay["model_catalog_json"]
			overrides = overrides || fileOverride
		} else if !os.IsNotExist(err) {
			return catalogPlan{}, errors.New("could not read selected Codex profile")
		}
		if overrides {
			return catalogPlan{}, errors.New("selected Codex profile overrides the model catalog; apply through the XIASS Tools Codex configuration page so profile overrides are updated together")
		}
	}
	root := map[string]any{"models": []any{}}
	models := []any{}
	configured := strings.TrimSpace(stringValue(config["model_catalog_json"]))
	if configured != "" {
		data, err := os.ReadFile(catalogPath(m.CodexHome, configured))
		if err == nil {
			var parseErr error
			root, models, parseErr = parseModelCatalog(data)
			if parseErr != nil {
				return catalogPlan{}, parseErr
			}
		} else if !os.IsNotExist(err) {
			return catalogPlan{}, errors.New("could not read configured Codex catalog")
		}
	}
	if len(models) == 0 && configured == "" {
		if data, err := os.ReadFile(filepath.Join(m.CodexHome, "models_cache.json")); err == nil {
			if cached, entries, err := parseModelCatalog(data); err == nil {
				root, models = cached, entries
			}
		}
	}
	templates := map[string]map[string]any{}
	unique := []any{}
	for _, raw := range models {
		entry := raw.(map[string]any)
		id := strings.TrimSpace(stringValue(entry["slug"]))
		if _, ok := templates[id]; !ok {
			templates[id] = entry
			unique = append(unique, entry)
		}
	}
	seen := map[string]bool{}
	for id := range templates {
		seen[id] = true
	}
	ids := append(append([]string{}, BuiltInCodexModels...), input.Model, input.ReviewModel)
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		family := map[string]string{"gpt-6-sol": "gpt-5.6-sol", "gpt-6-luna": "gpt-5.6-luna"}[id]
		template := templates[family]
		if template == nil && strings.HasPrefix(id, "gpt-") {
			template = templates["gpt-6-astra"]
		}
		entry := fallbackModel(id, input.ModelContextWindow)
		if template != nil {
			data, _ := json.Marshal(template)
			entry = map[string]any{}
			_ = json.Unmarshal(data, &entry)
		}
		entry["slug"], entry["display_name"], entry["description"] = id, id, id
		entry["visibility"], entry["supported_in_api"] = "list", true
		if id == "codex-auto-review" {
			entry["visibility"] = "hide"
		}
		for _, key := range []string{"upgrade", "availability_nux", "available_in_plans"} {
			delete(entry, key)
		}
		unique = append(unique, entry)
	}
	root["models"] = unique
	content, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return catalogPlan{}, errors.New("could not serialize Codex catalog")
	}
	content = append(content, '\n')
	return catalogPlan{path: filepath.Join(m.CodexHome, "xiass-tools", "model-catalogs", "catalog-"+sha256Hex(content)[:16]+".json"), content: content}, nil
}

func (plan catalogPlan) apply() error {
	if err := os.MkdirAll(filepath.Dir(plan.path), 0o700); err != nil {
		return err
	}
	if existing, err := os.ReadFile(plan.path); err == nil && string(existing) == string(plan.content) {
		return nil
	}
	if err := writeFileAtomic(plan.path, plan.content, 0o600); err != nil {
		return fmt.Errorf("write Codex model catalog: %w", err)
	}
	written, err := os.ReadFile(plan.path)
	if err != nil || string(written) != string(plan.content) {
		return errors.New("Codex model catalog read-back failed")
	}
	return nil
}

func configuredCatalogModels(home string, root map[string]any) []string {
	configured := strings.TrimSpace(stringValue(root["model_catalog_json"]))
	if configured == "" {
		return nil
	}
	data, err := os.ReadFile(catalogPath(home, configured))
	if err != nil {
		return nil
	}
	_, models, err := parseModelCatalog(data)
	if err != nil {
		return nil
	}
	ids := []string{}
	for _, raw := range models {
		entry := raw.(map[string]any)
		ids = append(ids, stringValue(entry["slug"]))
	}
	return ids
}
