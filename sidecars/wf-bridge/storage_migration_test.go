package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMigrateLegacyStorageDirectoryRenamesSoleDirectory(t *testing.T) {
	home := t.TempDir()
	legacy := filepath.Join(home, legacyStorageDirectoryNames[0])
	target := filepath.Join(home, xiassToolsStorageDirectoryName)
	if err := os.MkdirAll(filepath.Join(legacy, "backups"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "custom_models.json"), []byte("current"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := migrateLegacyStorageDirectory(legacy, target); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy directory still exists: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(target, "custom_models.json")); err != nil || string(data) != "current" {
		t.Fatalf("migrated config = %q, %v", data, err)
	}
}

func TestMigrateLegacyStorageDirectoryMergesNewestFiles(t *testing.T) {
	home := t.TempDir()
	legacy := filepath.Join(home, legacyStorageDirectoryNames[0])
	target := filepath.Join(home, xiassToolsStorageDirectoryName)
	if err := os.MkdirAll(filepath.Join(legacy, "backups"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(target, "backups"), 0o700); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-time.Hour)
	newTime := time.Now()
	legacyConfig := filepath.Join(legacy, "custom_models.json")
	targetConfig := filepath.Join(target, "custom_models.json")
	if err := os.WriteFile(legacyConfig, []byte("latest-models"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(legacyConfig, newTime, newTime); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(targetConfig, []byte("stale-models"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(targetConfig, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	legacySettings := filepath.Join(legacy, "settings.json")
	targetSettings := filepath.Join(target, "settings.json")
	if err := os.WriteFile(legacySettings, []byte("stale-settings"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(legacySettings, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(targetSettings, []byte("latest-settings"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(targetSettings, newTime, newTime); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "backups", "renderer.bak"), []byte("backup"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := migrateLegacyStorageDirectory(legacy, target); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy directory still exists: %v", err)
	}
	assertMigratedFile := func(path, expected string) {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil || string(data) != expected {
			t.Fatalf("%s = %q, %v", path, data, err)
		}
	}
	assertMigratedFile(targetConfig, "latest-models")
	assertMigratedFile(targetSettings, "latest-settings")
	assertMigratedFile(filepath.Join(target, "backups", "renderer.bak"), "backup")
}
