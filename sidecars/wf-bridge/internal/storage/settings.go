package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// StreamRecoverySettings control safe retries before an upstream generation
// has delivered any event to Antigravity. Once output begins, the proxy never
// replays that request; the user's visible chat and token budget take priority.
type StreamRecoverySettings struct {
	Enabled         bool `json:"enabled"`
	MaxAttempts     int  `json:"maxAttempts"`
	MaxDelaySeconds int  `json:"maxDelaySeconds"`
}

// UpdateSettings contains only local update preferences. The updater itself
// always verifies a release asset before it is allowed to start an installer.
type UpdateSettings struct {
	AutoCheck      bool   `json:"autoCheck"`
	SkippedVersion string `json:"skippedVersion"`
}

// OAuthSettings stores only non-secret convenience preferences. A public
// desktop Client ID identifies an OAuth application; it is not an access
// token, refresh token, or client secret. Tokens continue to live solely in
// the account store / platform credential flow.
type OAuthSettings struct {
	GoogleDesktopClientID string `json:"googleDesktopClientId,omitempty"`
}

// AppSettings is persisted in the same private application directory as the
// model list. SchemaVersion lets newly added options retain safe defaults for
// installations created by earlier releases.
type AppSettings struct {
	SchemaVersion  int                    `json:"schemaVersion"`
	StreamRecovery StreamRecoverySettings `json:"streamRecovery"`
	Updates        UpdateSettings         `json:"updates"`
	OAuth          OAuthSettings          `json:"oauth"`
}

const appSettingsSchemaVersion = 1

var settingsMu sync.RWMutex

func DefaultAppSettings() AppSettings {
	return AppSettings{
		SchemaVersion: appSettingsSchemaVersion,
		StreamRecovery: StreamRecoverySettings{
			Enabled: true, MaxAttempts: 2, MaxDelaySeconds: 20,
		},
		Updates: UpdateSettings{AutoCheck: true},
	}
}

// NormalizeAppSettings prevents a corrupted or hand-edited settings file from
// disabling automatic recovery accidentally or producing an unbounded retry
// loop. Deliberately setting Enabled=false remains supported.
func NormalizeAppSettings(settings AppSettings) AppSettings {
	defaults := DefaultAppSettings()
	if settings.SchemaVersion <= 0 {
		return defaults
	}
	settings.SchemaVersion = appSettingsSchemaVersion
	if settings.StreamRecovery.MaxAttempts <= 0 {
		settings.StreamRecovery.MaxAttempts = defaults.StreamRecovery.MaxAttempts
	}
	if settings.StreamRecovery.MaxAttempts > 10 {
		settings.StreamRecovery.MaxAttempts = 10
	}
	if settings.StreamRecovery.MaxDelaySeconds <= 0 {
		settings.StreamRecovery.MaxDelaySeconds = defaults.StreamRecovery.MaxDelaySeconds
	}
	if settings.StreamRecovery.MaxDelaySeconds > 120 {
		settings.StreamRecovery.MaxDelaySeconds = 120
	}
	settings.OAuth.GoogleDesktopClientID = normalizePublicOAuthClientID(settings.OAuth.GoogleDesktopClientID)
	return settings
}

func normalizePublicOAuthClientID(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 512 || strings.ContainsAny(value, "\r\n\x00") {
		return ""
	}
	return value
}

func appSettingsPath() string {
	return filepath.Join(storageDir, "settings.json")
}

func LoadAppSettings() (AppSettings, error) {
	settingsMu.RLock()
	defer settingsMu.RUnlock()
	return loadAppSettingsLocked()
}

func loadAppSettingsLocked() (AppSettings, error) {
	data, err := os.ReadFile(appSettingsPath())
	if os.IsNotExist(err) {
		return DefaultAppSettings(), nil
	}
	if err != nil {
		return DefaultAppSettings(), err
	}
	var settings AppSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		return DefaultAppSettings(), err
	}
	return NormalizeAppSettings(settings), nil
}

func SaveAppSettings(settings AppSettings) error {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	settings = NormalizeAppSettings(settings)
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(storageDir, 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(storageDir, ".settings-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(append(data, '\n')); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return replaceStorageFile(tempPath, appSettingsPath())
}
