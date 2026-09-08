package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"antigravity-wf-assistant/internal/totp"
)

type appTOTPStore struct{ values map[string]string }

type failingAppTOTPStore struct{}

func (failingAppTOTPStore) Set(_ string, _ string, _ string) error {
	return errors.New("credential secret=totp-private-value at /private/user/keychain")
}
func (failingAppTOTPStore) Get(_ string, _ string) (string, error) {
	return "", errors.New("credential secret=totp-private-value at /private/user/keychain")
}
func (failingAppTOTPStore) Delete(_ string, _ string) error {
	return errors.New("credential secret=totp-private-value at /private/user/keychain")
}

func (store *appTOTPStore) Set(_ string, account, secret string) error {
	if store.values == nil {
		store.values = map[string]string{}
	}
	store.values[account] = secret
	return nil
}
func (store *appTOTPStore) Get(_ string, account string) (string, error) {
	return store.values[account], nil
}
func (store *appTOTPStore) Delete(_ string, account string) error {
	delete(store.values, account)
	return nil
}

func TestAppTOTPStatusAndCodeRemainSecretFree(t *testing.T) {
	vault, err := totp.NewWithStore(t.TempDir(), &appTOTPStore{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{totpVault: vault}
	status := app.AddTOTPEntry(totp.ImportInput{Secret: "JBSWY3DPEHPK3PXP", Label: "XIASS"})
	if !status.OK || len(status.Entries) != 1 {
		t.Fatalf("status = %#v", status)
	}
	if status.Entries[0].ID == "" || status.Entries[0].Label != "XIASS" {
		t.Fatalf("entry = %#v", status.Entries[0])
	}
	code := app.GenerateTOTPCode(status.Entries[0].ID)
	if !code.OK || len(code.Code.Value) != 6 {
		t.Fatalf("code = %#v", code)
	}
}

func TestWriteNewSensitiveExportDoesNotOverwriteExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backup.json")
	if err := writeNewSensitiveExport(path, []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := writeNewSensitiveExport(path, []byte("second")); err == nil {
		t.Fatal("existing export was overwritten")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "first" {
		t.Fatalf("data = %q, %v", data, err)
	}
}

func TestTOTPBridgeDoesNotSerializeCredentialStoreErrors(t *testing.T) {
	vault, err := totp.NewWithStore(t.TempDir(), failingAppTOTPStore{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	status := (&App{totpVault: vault}).AddTOTPEntry(totp.ImportInput{
		Secret: "JBSWY3DPEHPK3PXP",
		Label:  "Test",
	})
	if status.OK {
		t.Fatalf("unexpected successful status: %#v", status)
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"totp-private-value", "/private/user", "credential secret"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("TOTP bridge leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestTOTPEncryptedImportBridgeRestoresMetadataWithoutLeakingSecret(t *testing.T) {
	source, err := totp.NewWithStore(t.TempDir(), &appTOTPStore{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Add(totp.ImportInput{Secret: "JBSWY3DPEHPK3PXP", Label: "XIASS backup"}); err != nil {
		t.Fatal(err)
	}
	payload, err := source.ExportEncrypted("long-enough-export-password")
	if err != nil {
		t.Fatal(err)
	}
	target, err := totp.NewWithStore(t.TempDir(), &appTOTPStore{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	status := (&App{totpVault: target}).importTOTPEncryptedData(payload, "long-enough-export-password")
	if !status.OK || len(status.Entries) != 1 || status.Entries[0].Label != "XIASS backup" {
		t.Fatalf("status = %#v", status)
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "JBSWY3DPEHPK3PXP") {
		t.Fatalf("TOTP import bridge leaked a secret: %s", encoded)
	}
}

func TestTOTPEncryptedImportBridgeRedactsCredentialStoreFailure(t *testing.T) {
	source, err := totp.NewWithStore(t.TempDir(), &appTOTPStore{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Add(totp.ImportInput{Secret: "JBSWY3DPEHPK3PXP", Label: "XIASS backup"}); err != nil {
		t.Fatal(err)
	}
	payload, err := source.ExportEncrypted("long-enough-export-password")
	if err != nil {
		t.Fatal(err)
	}
	target, err := totp.NewWithStore(t.TempDir(), failingAppTOTPStore{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	status := (&App{totpVault: target}).importTOTPEncryptedData(payload, "long-enough-export-password")
	if status.OK {
		t.Fatalf("unexpected successful status: %#v", status)
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"JBSWY3DPEHPK3PXP", "totp-private-value", "/private/user", "credential secret"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("TOTP import bridge leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestReadSensitiveTOTPImportAcceptsRegularFileWithinLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "totp-backup.json")
	want := []byte(`{"version":1}`)
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := readSensitiveTOTPImport(path, int64(len(want)))
	if err != nil || string(data) != string(want) {
		t.Fatalf("data = %q, err = %v", data, err)
	}
	wipeTOTPImportData(data)
	if _, err := readSensitiveTOTPImport(path, int64(len(want)-1)); err == nil {
		t.Fatal("oversized import was accepted")
	}
}
