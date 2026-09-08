package storage

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestHelperTransferRoundTripIncludesAccountsModelsAndSettings(t *testing.T) {
	dir := t.TempDir()
	Init(dir)
	account := UpstreamAccount{ID: "account-1", Name: "primary", Provider: "openai", Type: "api_key", APIURL: "https://example.test", APIKey: "secret-key", Enabled: true}
	if err := SaveUpstreamAccount(account); err != nil {
		t.Fatal(err)
	}
	model := CustomModel{Name: "models/test", DisplayName: "Test", Provider: "openai", APIURL: "https://example.test", ExternalModelName: "test", AccountIDs: []string{account.ID}}
	if err := SaveModels([]CustomModel{model}); err != nil {
		t.Fatal(err)
	}
	settings := DefaultAppSettings()
	settings.StreamRecovery.MaxAttempts = 4
	if err := SaveAppSettings(settings); err != nil {
		t.Fatal(err)
	}

	bundle, err := ExportHelperTransferBundle()
	if err != nil {
		t.Fatalf("export helper transfer: %v", err)
	}
	if bundle.Summary.AccountCount != 1 || bundle.Summary.ModelCount != 1 || bundle.Accounts[0].APIKey != "secret-key" {
		t.Fatalf("incomplete helper transfer: %#v", bundle.Summary)
	}
	bundle.Accounts[0].Name = "restored"
	bundle.Models[0].DisplayName = "Restored Model"
	result, err := RestoreHelperTransferBundle(bundle)
	if err != nil || !result.OK || result.RolledBack {
		t.Fatalf("restore helper transfer: %#v %v", result, err)
	}
	accounts, _ := LoadUpstreamAccounts()
	models, _ := LoadModels()
	restoredSettings, _ := LoadAppSettings()
	if accounts[0].Name != "restored" || models[0].DisplayName != "Restored Model" || restoredSettings.StreamRecovery.MaxAttempts != 4 {
		t.Fatalf("helper transfer did not restore all sections")
	}
}

func TestHelperTransferRestoreRollsBackEarlierFiles(t *testing.T) {
	dir := t.TempDir()
	Init(dir)
	account := UpstreamAccount{ID: "account-1", Name: "before", Provider: "openai", Type: "api_key", APIURL: "https://example.test", APIKey: "secret-key", Enabled: true}
	if err := SaveUpstreamAccount(account); err != nil {
		t.Fatal(err)
	}
	model := CustomModel{Name: "models/test", DisplayName: "Before", Provider: "openai", APIURL: "https://example.test", ExternalModelName: "test", AccountIDs: []string{account.ID}}
	if err := SaveModels([]CustomModel{model}); err != nil {
		t.Fatal(err)
	}
	bundle, _ := ExportHelperTransferBundle()
	bundle.Accounts[0].Name = "after"
	bundle.Models[0].DisplayName = "After"
	originalWriter := helperTransferWriteFile
	calls := 0
	helperTransferWriteFile = func(path string, data []byte) error {
		calls++
		if calls == 2 {
			return errors.New("test write failure")
		}
		return writeHelperTransferFile(path, data)
	}
	t.Cleanup(func() { helperTransferWriteFile = originalWriter })
	result, err := RestoreHelperTransferBundle(bundle)
	if err == nil || !result.RolledBack {
		t.Fatalf("expected rollback, got %#v %v", result, err)
	}
	accounts, _ := LoadUpstreamAccounts()
	models, _ := LoadModels()
	if accounts[0].Name != "before" || models[0].DisplayName != "Before" {
		t.Fatal("failed restore changed existing helper data")
	}
}

func TestHelperTransferExportRejectsSymlinkedOwnedFile(t *testing.T) {
	dir := t.TempDir()
	Init(dir)
	target := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(target, []byte(`{"accounts":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "upstream_accounts.json")); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("cannot create symlink: %v", err)
		}
		t.Fatal(err)
	}
	if _, err := ExportHelperTransferBundle(); err == nil {
		t.Fatal("helper export followed a symlink")
	}
}
