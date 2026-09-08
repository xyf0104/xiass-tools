package codexconfig

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// RemoveXIASSProvider removes only the stable XIASS Tools Codex Provider.
//
// It deliberately does not use Manager.ProviderID: an explicit disconnect
// must never silently remove a custom provider, a legacy provider, or another
// application's entry. When xiass_tools is the active
// provider it also clears the three coupled root selections so Codex cannot be
// left pointing at a provider that no longer exists. All other TOML data,
// including MCP, Desktop, unknown settings, and unrelated providers is kept.
//
// The operation follows the same locked backup, atomic-write, read-back and
// rollback lifecycle as Apply. Configurations whose textual structure cannot
// be removed without ambiguity fail closed before a backup or write begins.
func (m *Manager) RemoveXIASSProvider() (RemoveResult, error) {
	if err := m.validatePaths(); err != nil {
		return RemoveResult{}, err
	}
	// Run a complete read-only preflight before acquiring the operation lock.
	// AcquireOperationLock creates the XIASS Tools data directory and lock file;
	// a verified no-op must not leave either artifact in a user's Codex home.
	// The state is planned again after locking, so a concurrent config change can
	// never turn this preliminary observation into an unsafe write.
	preflightOriginal, preflightExisted, _, err := readRegularFile(m.ConfigPath)
	if err != nil {
		return RemoveResult{}, err
	}
	_, preflightPlan, err := planXIASSProviderRemoval(preflightOriginal, preflightExisted)
	if err != nil {
		return RemoveResult{}, err
	}
	if !preflightPlan.changed {
		return RemoveResult{WasActive: preflightPlan.wasActive}, nil
	}

	var result RemoveResult
	err = m.withLock(func() error {
		original, existed, mode, err := readRegularFile(m.ConfigPath)
		if err != nil {
			return err
		}
		updated, plan, err := planXIASSProviderRemoval(original, existed)
		if err != nil {
			return err
		}
		result.WasActive = plan.wasActive
		if !plan.changed {
			return nil
		}

		backup, err := m.createBackup(original, existed, mode, "remove_xiass_provider")
		if err != nil {
			return fmt.Errorf("create config backup: %w", err)
		}
		if err := ensureFileUnchanged(m.ConfigPath, original, existed); err != nil {
			return err
		}
		if err := writeFileAtomic(m.ConfigPath, updated, secureMode(mode)); err != nil {
			return rollbackMutation(fmt.Errorf("remove XIASS Tools provider from config.toml: %w", err), m.ConfigPath, original, existed, mode)
		}

		written, writtenExists, _, err := readRegularFile(m.ConfigPath)
		if err != nil || !writtenExists {
			cause := errors.New("read back config.toml failed")
			if err != nil {
				cause = fmt.Errorf("read back config.toml: %w", err)
			}
			return rollbackMutation(cause, m.ConfigPath, original, existed, mode)
		}
		if err := verifyXIASSProviderRemoval(original, written, plan); err != nil {
			return rollbackMutation(fmt.Errorf("written removal verification failed: %w", err), m.ConfigPath, original, existed, mode)
		}

		backup.AppliedSHA256 = sha256Hex(written)
		if err := m.writeManifest(backup); err != nil {
			return rollbackMutation(fmt.Errorf("record verified backup metadata: %w", err), m.ConfigPath, original, existed, mode)
		}
		result = RemoveResult{
			BackupID:  backup.ID,
			ConfigSHA: backup.AppliedSHA256,
			Removed:   true,
			WasActive: plan.wasActive,
		}
		return nil
	})
	return result, err
}

// planXIASSProviderRemoval performs every check that is safe before a lock or
// backup directory exists. A caller that subsequently writes must call it a
// second time after obtaining the operation lock.
func planXIASSProviderRemoval(original []byte, existed bool) ([]byte, xiassProviderRemovalPlan, error) {
	if !existed || len(bytes.TrimSpace(original)) == 0 {
		return append([]byte(nil), original...), xiassProviderRemovalPlan{}, nil
	}
	if err := validateTOML(original); err != nil {
		return nil, xiassProviderRemovalPlan{}, fmt.Errorf("existing config.toml is invalid; no changes were made: %w", err)
	}
	updated, plan, err := removeXIASSProviderConfig(original)
	if err != nil {
		return nil, xiassProviderRemovalPlan{}, err
	}
	if plan.changed {
		if err := verifyXIASSProviderRemoval(original, updated, plan); err != nil {
			return nil, xiassProviderRemovalPlan{}, fmt.Errorf("generated removal verification failed: %w", err)
		}
	}
	return updated, plan, nil
}

type xiassProviderRemovalPlan struct {
	changed            bool
	wasActive          bool
	hadProvider        bool
	foundProviderTable bool
}

// removeXIASSProviderConfig keeps the original byte layout for every line it
// does not own. It accepts a deliberately narrow, line-safe TOML subset only
// when a removal is needed. This is preferable to re-serializing config.toml:
// comments, ordering, unsupported extensions, and unrelated user settings
// remain untouched instead of being guessed at or normalized by this tool.
func removeXIASSProviderConfig(original []byte) ([]byte, xiassProviderRemovalPlan, error) {
	var root map[string]any
	if err := toml.Unmarshal(original, &root); err != nil {
		return nil, xiassProviderRemovalPlan{}, fmt.Errorf("existing config.toml is invalid; no changes were made: %w", err)
	}

	plan, err := identifyXIASSProviderRemoval(root)
	if err != nil {
		return nil, xiassProviderRemovalPlan{}, err
	}
	if !plan.wasActive && !plan.hadProvider {
		return append([]byte(nil), original...), plan, nil
	}

	text := string(original)
	lines := strings.Split(text, "\n")
	body := make([]string, 0, len(lines))
	inTopLevel := true
	skipXIASSProvider := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			if !skipXIASSProvider {
				body = append(body, line)
			}
			continue
		}
		if strings.HasPrefix(trimmed, "[") {
			path, ok := tomlTablePath(line)
			if !ok {
				return nil, xiassProviderRemovalPlan{}, errors.New("unsupported TOML table structure; no changes were made")
			}
			inTopLevel = false
			skipXIASSProvider = isManagedProviderPath(path, []string{DefaultProviderID})
			if skipXIASSProvider {
				plan.foundProviderTable = true
				plan.changed = true
				continue
			}
			body = append(body, line)
			continue
		}

		key, ok := tomlAssignmentKey(line)
		if !ok || !isSingleLineTOMLAssignment(line) {
			return nil, xiassProviderRemovalPlan{}, errors.New("unsupported TOML assignment structure; no changes were made")
		}
		if skipXIASSProvider {
			continue
		}
		if inTopLevel && plan.wasActive && isActiveXIASSSelectionKey(key) {
			plan.changed = true
			continue
		}
		body = append(body, line)
	}

	if plan.hadProvider && !plan.foundProviderTable {
		// A dotted inline table or another representation can be valid TOML but
		// is not safe to surgically edit while promising byte preservation.
		return nil, xiassProviderRemovalPlan{}, errors.New("unsupported XIASS Tools provider TOML representation; no changes were made")
	}
	if plan.wasActive && !plan.changed {
		return nil, xiassProviderRemovalPlan{}, errors.New("active XIASS Tools selection could not be safely removed; no changes were made")
	}
	return []byte(strings.Join(body, "\n")), plan, nil
}

func identifyXIASSProviderRemoval(root map[string]any) (xiassProviderRemovalPlan, error) {
	plan := xiassProviderRemovalPlan{}
	if value, present := root["model_provider"]; present {
		provider, ok := value.(string)
		if !ok {
			return plan, errors.New("unsupported model_provider value; no changes were made")
		}
		plan.wasActive = provider == DefaultProviderID
	}
	providersValue, present := root["model_providers"]
	if !present {
		return plan, nil
	}
	providers, ok := mapValue(providersValue)
	if !ok {
		return plan, errors.New("unsupported model_providers value; no changes were made")
	}
	value, present := providers[DefaultProviderID]
	if !present {
		return plan, nil
	}
	if _, ok := mapValue(value); !ok {
		return plan, errors.New("unsupported XIASS Tools provider value; no changes were made")
	}
	plan.hadProvider = true
	return plan, nil
}

func isActiveXIASSSelectionKey(key string) bool {
	switch key {
	case "model_provider", "model", "review_model":
		return true
	default:
		return false
	}
}

// isSingleLineTOMLAssignment rejects multiline strings, arrays, and inline
// tables. Their continuation lines cannot be proven to belong to one specific
// top-level key with the lightweight surgery used here, so disconnecting fails
// closed rather than risking an unrelated setting.
func isSingleLineTOMLAssignment(line string) bool {
	trimmed := strings.TrimSpace(line)
	equals := indexTOMLDelimiter(trimmed, '=')
	if equals <= 0 {
		return false
	}
	value := trimmed[equals+1:]
	inBasic := false
	inLiteral := false
	escaped := false
	depth := 0
	for index := 0; index < len(value); index++ {
		character := value[index]
		if inBasic {
			if escaped {
				escaped = false
				continue
			}
			if character == '\\' {
				escaped = true
				continue
			}
			if character == '"' {
				inBasic = false
			}
			continue
		}
		if inLiteral {
			if character == '\'' {
				inLiteral = false
			}
			continue
		}
		if character == '#' {
			break
		}
		if strings.HasPrefix(value[index:], `"""`) || strings.HasPrefix(value[index:], `'''`) {
			return false
		}
		switch character {
		case '"':
			inBasic = true
		case '\'':
			inLiteral = true
		case '[', '{':
			depth++
		case ']', '}':
			depth--
			if depth < 0 {
				return false
			}
		}
	}
	return !inBasic && !inLiteral && !escaped && depth == 0
}

func verifyXIASSProviderRemoval(original, updated []byte, plan xiassProviderRemovalPlan) error {
	var expected map[string]any
	if err := toml.Unmarshal(original, &expected); err != nil {
		return err
	}
	var actual map[string]any
	if err := toml.Unmarshal(updated, &actual); err != nil {
		return err
	}

	if providers, ok := mapValue(expected["model_providers"]); ok {
		delete(providers, DefaultProviderID)
	}
	if plan.wasActive {
		delete(expected, "model_provider")
		delete(expected, "model")
		delete(expected, "review_model")
	}
	normalizeEmptyModelProviders(expected)
	normalizeEmptyModelProviders(actual)

	if providers, ok := mapValue(actual["model_providers"]); ok {
		if _, exists := providers[DefaultProviderID]; exists {
			return errors.New("XIASS Tools provider remains")
		}
	}
	if plan.wasActive {
		for _, key := range []string{"model_provider", "model", "review_model"} {
			if _, exists := actual[key]; exists {
				return fmt.Errorf("active selection %s remains", key)
			}
		}
	}
	// go-toml may decode an otherwise empty document as either a nil or an
	// allocated empty map. Both represent the same intentional result after the
	// final xiass_tools entry and active selection have been removed.
	if len(expected) == 0 && len(actual) == 0 {
		return nil
	}
	if !reflect.DeepEqual(expected, actual) {
		return errors.New("unmanaged TOML values changed")
	}
	return nil
}

func normalizeEmptyModelProviders(root map[string]any) {
	providers, ok := mapValue(root["model_providers"])
	if ok && len(providers) == 0 {
		delete(root, "model_providers")
	}
}
