package codexconfig

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// firstPartyLegacyProviderMigrationIDs is intentionally not derived from a
// Manager option. Migration is a much narrower operation than Apply: a custom
// build must never persuade XIASS Tools to adopt or rewrite a third-party
// Provider merely because it has been labelled as "legacy" elsewhere.
var firstPartyLegacyProviderMigrationIDs = []string{"xiass", "codex_local_access"}

// InspectLegacyProviderMigration performs a read-only, redacted eligibility
// check for the one supported legacy Provider migration. It does not create a
// backup, lock, configuration file, directory, recovery point, or any other
// artifact. A configuration that is valid TOML but cannot be edited with a
// byte-preserving proof simply returns Available=false; the native mutation
// re-runs the stricter planner and fails closed before it writes anything.
func (m *Manager) InspectLegacyProviderMigration() (LegacyProviderMigrationStatus, error) {
	if err := m.validatePaths(); err != nil {
		return LegacyProviderMigrationStatus{}, err
	}
	original, existed, _, err := readRegularFile(m.ConfigPath)
	if err != nil {
		return LegacyProviderMigrationStatus{}, err
	}
	_, plan, err := planLegacyProviderMigration(original, existed)
	if err != nil {
		return LegacyProviderMigrationStatus{}, err
	}
	return plan.status, nil
}

// MigrateLegacyProvider moves exactly one known first-party Provider table
// (xiass or codex_local_access) to the fixed xiass_tools ID. It intentionally
// does not read auth.json, OAuth, cookies, sessions, history, arbitrary
// Provider data, or process command lines. The old Provider's opaque table is
// copied byte-for-byte under its new fixed table heading; no key is decoded,
// returned, logged, or placed in a result.
//
// A direct migration is never allowed while Codex is running. The guard is
// checked before a lock is created and again inside the locked transaction.
// The explicit Desktop lifecycle bridge first stops Codex with user
// confirmation, then calls this same operation after a fresh safety check.
func (m *Manager) MigrateLegacyProvider() (LegacyProviderMigrationResult, error) {
	if err := m.validatePaths(); err != nil {
		return LegacyProviderMigrationResult{}, err
	}

	// Read-only preflight happens before the lock because acquiring a lock
	// creates XIASS Tools storage. A no-op must leave no managed artifacts.
	preflightOriginal, preflightExisted, _, err := readRegularFile(m.ConfigPath)
	if err != nil {
		return LegacyProviderMigrationResult{}, err
	}
	_, preflight, err := planLegacyProviderMigration(preflightOriginal, preflightExisted)
	if err != nil {
		return LegacyProviderMigrationResult{}, err
	}
	if !preflight.status.Available {
		return LegacyProviderMigrationResult{}, nil
	}
	if err := m.requireCodexHistoryWriteSafety(); err != nil {
		return LegacyProviderMigrationResult{}, err
	}

	var result LegacyProviderMigrationResult
	err = m.withLock(func() error {
		// A process may have started after preflight. A second guard is the
		// last check before we even re-read the mutable config file.
		if err := m.requireCodexHistoryWriteSafety(); err != nil {
			return err
		}
		original, existed, mode, err := readRegularFile(m.ConfigPath)
		if err != nil {
			return err
		}
		updated, plan, err := planLegacyProviderMigration(original, existed)
		if err != nil {
			return err
		}
		if !plan.status.Available {
			return nil
		}

		backup, err := m.createBackup(original, existed, mode, "migrate_legacy_provider")
		if err != nil {
			return fmt.Errorf("create config backup: %w", err)
		}
		if err := ensureFileUnchanged(m.ConfigPath, original, existed); err != nil {
			return err
		}
		write := m.legacyProviderMigrationWrite
		if write == nil {
			write = writeFileAtomic
		}
		if err := write(m.ConfigPath, updated, secureMode(mode)); err != nil {
			return rollbackMutation(fmt.Errorf("migrate legacy Codex Provider: %w", err), m.ConfigPath, original, existed, mode)
		}

		written, writtenExists, _, err := readRegularFile(m.ConfigPath)
		if err != nil || !writtenExists {
			cause := errors.New("read back config.toml failed")
			if err != nil {
				cause = fmt.Errorf("read back config.toml: %w", err)
			}
			return rollbackMutation(cause, m.ConfigPath, original, existed, mode)
		}
		if err := verifyLegacyProviderMigration(original, written, plan); err != nil {
			return rollbackMutation(fmt.Errorf("written legacy Provider migration verification failed: %w", err), m.ConfigPath, original, existed, mode)
		}

		backup.AppliedSHA256 = sha256Hex(written)
		if err := m.writeManifest(backup); err != nil {
			return rollbackMutation(fmt.Errorf("record verified backup metadata: %w", err), m.ConfigPath, original, existed, mode)
		}
		result = LegacyProviderMigrationResult{
			BackupID:   backup.ID,
			ConfigSHA:  backup.AppliedSHA256,
			Migrated:   true,
			ProviderID: plan.status.ProviderID,
			WasActive:  plan.status.WasActive,
		}
		return nil
	})
	return result, err
}

type legacyProviderMigrationPlan struct {
	status LegacyProviderMigrationStatus
}

// planLegacyProviderMigration has no side effects. Any caller that later
// writes must run it again while holding the operation lock.
func planLegacyProviderMigration(original []byte, existed bool) ([]byte, legacyProviderMigrationPlan, error) {
	if !existed || len(bytes.TrimSpace(original)) == 0 {
		return append([]byte(nil), original...), legacyProviderMigrationPlan{}, nil
	}
	if err := validateTOML(original); err != nil {
		return nil, legacyProviderMigrationPlan{}, fmt.Errorf("existing config.toml is invalid; no changes were made: %w", err)
	}
	updated, plan, err := migrateLegacyProviderConfig(original)
	if err != nil {
		return nil, legacyProviderMigrationPlan{}, err
	}
	if plan.status.Available {
		if err := verifyLegacyProviderMigration(original, updated, plan); err != nil {
			return nil, legacyProviderMigrationPlan{}, fmt.Errorf("generated legacy Provider migration verification failed: %w", err)
		}
	}
	return updated, plan, nil
}

// migrateLegacyProviderConfig performs strict, source-scoped textual surgery.
// It never serializes arbitrary TOML, never reads a secret value into a result,
// and keeps unrelated bytes unchanged. Multiline/ambiguous forms fail closed
// because there is no safe way to prove their ownership line-by-line.
func migrateLegacyProviderConfig(original []byte) ([]byte, legacyProviderMigrationPlan, error) {
	var root map[string]any
	if err := toml.Unmarshal(original, &root); err != nil {
		return nil, legacyProviderMigrationPlan{}, fmt.Errorf("existing config.toml is invalid; no changes were made: %w", err)
	}
	plan, err := identifyLegacyProviderMigration(root)
	if err != nil {
		return nil, legacyProviderMigrationPlan{}, err
	}
	if !plan.status.Available {
		return append([]byte(nil), original...), plan, nil
	}

	text := string(original)
	lines := strings.Split(text, "\n")
	body := make([]string, 0, len(lines))
	inTopLevel := true
	inLegacyProvider := false
	foundLegacyRootTable := false
	foundActiveSelection := !plan.status.WasActive

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			body = append(body, line)
			continue
		}
		if strings.HasPrefix(trimmed, "[") {
			if strings.HasPrefix(trimmed, "[[") {
				return nil, legacyProviderMigrationPlan{}, errors.New("unsupported TOML array-table structure; no changes were made")
			}
			path, ok := tomlTablePath(line)
			if !ok {
				return nil, legacyProviderMigrationPlan{}, errors.New("unsupported TOML table structure; no changes were made")
			}
			inTopLevel = false
			inLegacyProvider = isExactLegacyProviderPath(path, plan.status.ProviderID)
			if inLegacyProvider {
				if len(path) == 2 {
					foundLegacyRootTable = true
				}
				rewritten, err := rewriteLegacyProviderTableHeader(line, path)
				if err != nil {
					return nil, legacyProviderMigrationPlan{}, err
				}
				body = append(body, rewritten)
				continue
			}
			body = append(body, line)
			continue
		}

		key, ok := tomlAssignmentKey(line)
		if !ok || !isSingleLineTOMLAssignment(line) {
			return nil, legacyProviderMigrationPlan{}, errors.New("unsupported TOML assignment structure; no changes were made")
		}
		if inTopLevel && plan.status.WasActive && key == "model_provider" {
			body = append(body, rewriteLegacyModelProviderAssignment(line))
			foundActiveSelection = true
			continue
		}
		body = append(body, line)
	}
	if !foundLegacyRootTable {
		return nil, legacyProviderMigrationPlan{}, errors.New("unsupported legacy Provider TOML representation; no changes were made")
	}
	if !foundActiveSelection {
		return nil, legacyProviderMigrationPlan{}, errors.New("active legacy Provider selection could not be safely migrated; no changes were made")
	}
	return []byte(strings.Join(body, "\n")), plan, nil
}

// identifyLegacyProviderMigration intentionally looks up only fixed,
// first-party IDs. It does not iterate or copy arbitrary Provider values.
func identifyLegacyProviderMigration(root map[string]any) (legacyProviderMigrationPlan, error) {
	plan := legacyProviderMigrationPlan{}
	activeProvider, err := legacyProviderMigrationActiveProvider(root)
	if err != nil {
		return plan, err
	}
	if err := validateLegacyProviderMigrationPreservedSettings(root); err != nil {
		return plan, err
	}

	providersValue, present := root["model_providers"]
	if !present {
		if isFirstPartyLegacyProviderID(activeProvider) {
			return plan, errors.New("active legacy Provider table is missing; no changes were made")
		}
		return plan, nil
	}
	providers, ok := mapValue(providersValue)
	if !ok {
		return plan, errors.New("unsupported model_providers value; no changes were made")
	}
	if _, exists := providers[DefaultProviderID]; exists {
		// The current fixed entry wins. Do not overwrite or inspect its data.
		return plan, nil
	}

	candidates := make([]string, 0, 1)
	for _, providerID := range firstPartyLegacyProviderMigrationIDs {
		value, exists := providers[providerID]
		if !exists {
			continue
		}
		if _, ok := mapValue(value); !ok {
			return plan, errors.New("unsupported legacy Provider value; no changes were made")
		}
		candidates = append(candidates, providerID)
	}
	if len(candidates) == 0 {
		if isFirstPartyLegacyProviderID(activeProvider) {
			return plan, errors.New("active legacy Provider table is missing; no changes were made")
		}
		return plan, nil
	}
	if len(candidates) != 1 {
		// Two credentials can be legitimate historical data. Choosing one or
		// deleting either would be a hidden account-selection decision.
		return plan, errors.New("multiple legacy Providers are present; no changes were made")
	}
	if isFirstPartyLegacyProviderID(activeProvider) && activeProvider != candidates[0] {
		return plan, errors.New("active legacy Provider is ambiguous; no changes were made")
	}
	if activeProvider == DefaultProviderID {
		return plan, errors.New("current Provider selection is incomplete; no changes were made")
	}

	plan.status = LegacyProviderMigrationStatus{
		Available:  true,
		ProviderID: candidates[0],
		WasActive:  activeProvider == candidates[0],
	}
	return plan, nil
}

func legacyProviderMigrationActiveProvider(root map[string]any) (string, error) {
	value, present := root["model_provider"]
	if !present {
		return "", nil
	}
	provider, ok := value.(string)
	if !ok {
		return "", errors.New("unsupported model_provider value; no changes were made")
	}
	return provider, nil
}

// The migration changes only the Provider-ID selection. These values are kept
// byte-for-byte, but their types are validated first so the migration never
// claims it preserved a malformed model, review, context, or search setting.
func validateLegacyProviderMigrationPreservedSettings(root map[string]any) error {
	for _, key := range []string{"model", "review_model", "web_search"} {
		if value, present := root[key]; present {
			if _, ok := value.(string); !ok {
				return fmt.Errorf("unsupported %s value; no changes were made", key)
			}
		}
	}
	for _, key := range []string{"model_context_window", "model_auto_compact_token_limit"} {
		if value, present := root[key]; present {
			if _, ok := integerValue(value); !ok {
				return fmt.Errorf("unsupported %s value; no changes were made", key)
			}
		}
	}
	return nil
}

func isFirstPartyLegacyProviderID(providerID string) bool {
	for _, candidate := range firstPartyLegacyProviderMigrationIDs {
		if providerID == candidate {
			return true
		}
	}
	return false
}

func isExactLegacyProviderPath(path []string, providerID string) bool {
	return len(path) >= 2 && path[0] == "model_providers" && path[1] == providerID && isFirstPartyLegacyProviderID(providerID)
}

func rewriteLegacyProviderTableHeader(line string, path []string) (string, error) {
	if !isExactLegacyProviderPath(path, path[1]) {
		return "", errors.New("unsupported legacy Provider table path; no changes were made")
	}
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "[[") || !strings.HasPrefix(trimmed, "[") {
		return "", errors.New("unsupported legacy Provider table structure; no changes were made")
	}
	close := findTableClose(trimmed[1:], "]")
	if close < 0 {
		return "", errors.New("unsupported legacy Provider table structure; no changes were made")
	}
	close++
	path = append([]string(nil), path...)
	path[1] = DefaultProviderID
	leadingLength := len(line) - len(strings.TrimLeft(line, " \t"))
	leading := line[:leadingLength]
	lineEnding := ""
	if strings.HasSuffix(line, "\r") {
		lineEnding = "\r"
	}
	return leading + "[" + formatTOMLPath(path) + "]" + trimmed[close+1:] + lineEnding, nil
}

func formatTOMLPath(path []string) string {
	parts := make([]string, 0, len(path))
	for _, part := range path {
		if providerIDPattern.MatchString(part) {
			parts = append(parts, part)
			continue
		}
		parts = append(parts, strconv.Quote(part))
	}
	return strings.Join(parts, ".")
}

func rewriteLegacyModelProviderAssignment(line string) string {
	leadingLength := len(line) - len(strings.TrimLeft(line, " \t"))
	leading := line[:leadingLength]
	comment := tomlCommentSuffix(line)
	lineEnding := ""
	if strings.HasSuffix(line, "\r") && !strings.HasSuffix(comment, "\r") {
		lineEnding = "\r"
	}
	return leading + `model_provider = "` + DefaultProviderID + `"` + comment + lineEnding
}

func tomlCommentSuffix(line string) string {
	inBasic := false
	inLiteral := false
	escaped := false
	for index := 0; index < len(line); index++ {
		character := line[index]
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
		switch character {
		case '"':
			inBasic = true
		case '\'':
			inLiteral = true
		case '#':
			return " " + strings.TrimLeft(line[index:], " \t")
		}
	}
	return ""
}

func verifyLegacyProviderMigration(original, updated []byte, plan legacyProviderMigrationPlan) error {
	if !plan.status.Available || !isFirstPartyLegacyProviderID(plan.status.ProviderID) {
		return errors.New("legacy Provider migration plan is not available")
	}
	var expected map[string]any
	if err := toml.Unmarshal(original, &expected); err != nil {
		return err
	}
	var actual map[string]any
	if err := toml.Unmarshal(updated, &actual); err != nil {
		return err
	}

	providers, ok := mapValue(expected["model_providers"])
	if !ok {
		return errors.New("legacy Provider table is missing")
	}
	legacy, exists := providers[plan.status.ProviderID]
	if !exists {
		return errors.New("legacy Provider table is missing")
	}
	if _, exists := providers[DefaultProviderID]; exists {
		return errors.New("current Provider table already existed")
	}
	delete(providers, plan.status.ProviderID)
	providers[DefaultProviderID] = legacy
	if plan.status.WasActive {
		expected["model_provider"] = DefaultProviderID
	}

	actualProviders, ok := mapValue(actual["model_providers"])
	if !ok {
		return errors.New("migrated Provider table is missing")
	}
	if _, exists := actualProviders[plan.status.ProviderID]; exists {
		return errors.New("legacy Provider table remains")
	}
	if _, exists := actualProviders[DefaultProviderID]; !exists {
		return errors.New("current Provider table is missing")
	}
	if plan.status.WasActive && stringValue(actual["model_provider"]) != DefaultProviderID {
		return errors.New("current Provider selection was not migrated")
	}
	if !reflect.DeepEqual(expected, actual) {
		return errors.New("unmanaged TOML values changed")
	}
	return nil
}
