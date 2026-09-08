package totp

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type memoryStore struct {
	mu        sync.Mutex
	values    map[string]string
	setErr    error
	getErr    error
	deleteErr error
}

type failAfterSetStore struct {
	mu      sync.Mutex
	values  map[string]string
	failAt  int
	setCall int
}

// cleanupRecoveryStore simulates the one failure mode that must never leave a
// credential invisible forever: a multi-entry import cannot remove its newly
// written Keychain/Credential Manager records immediately. The vault journal
// must retain the non-secret IDs and finish cleanup on the next safe operation.
type cleanupRecoveryStore struct {
	mu           sync.Mutex
	values       map[string]string
	failAt       int
	setCall      int
	failDeletion bool
}

func (store *cleanupRecoveryStore) Set(_ string, account, secret string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.setCall++
	if store.failAt > 0 && store.setCall == store.failAt {
		return errors.New("credential store write failed")
	}
	if store.values == nil {
		store.values = map[string]string{}
	}
	store.values[account] = secret
	return nil
}

func (store *cleanupRecoveryStore) Get(_ string, account string) (string, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	value, ok := store.values[account]
	if !ok {
		return "", errors.New("not found")
	}
	return value, nil
}

func (store *cleanupRecoveryStore) Delete(_ string, account string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.failDeletion {
		return errors.New("credential store deletion failed")
	}
	delete(store.values, account)
	return nil
}

func (store *failAfterSetStore) Set(_ string, account, secret string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.setCall++
	if store.failAt > 0 && store.setCall == store.failAt {
		return errors.New("credential store write failed")
	}
	if store.values == nil {
		store.values = map[string]string{}
	}
	store.values[account] = secret
	return nil
}

func (store *failAfterSetStore) Get(_ string, account string) (string, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	value, ok := store.values[account]
	if !ok {
		return "", errors.New("not found")
	}
	return value, nil
}

func (store *failAfterSetStore) Delete(_ string, account string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	delete(store.values, account)
	return nil
}

func (store *memoryStore) Set(_ string, account, secret string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.setErr != nil {
		return store.setErr
	}
	if store.values == nil {
		store.values = map[string]string{}
	}
	store.values[account] = secret
	return nil
}

func (store *memoryStore) Get(_ string, account string) (string, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.getErr != nil {
		return "", store.getErr
	}
	value, ok := store.values[account]
	if !ok {
		return "", errors.New("not found")
	}
	return value, nil
}

func (store *memoryStore) Delete(_ string, account string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.deleteErr != nil {
		return store.deleteErr
	}
	delete(store.values, account)
	return nil
}

func TestVaultAddsMetadataWithoutPersistingSecret(t *testing.T) {
	store := &memoryStore{}
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	vault, err := NewWithStore(t.TempDir(), store, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	entry, err := vault.Add(ImportInput{URI: "otpauth://totp/XIASS:alice?secret=JBSWY3DPEHPK3PXP&issuer=XIASS"})
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID == "" || entry.Label != "XIASS:alice" || entry.Issuer != "XIASS" || entry.Account != "alice" {
		t.Fatalf("entry = %#v", entry)
	}
	entries, err := vault.List()
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries = %#v, %v", entries, err)
	}
	data, exists, err := readRegularFile(vault.index)
	if err != nil || !exists || !strings.Contains(string(data), entry.ID) || strings.Contains(string(data), "JBSWY3DPEHPK3PXP") {
		t.Fatalf("metadata data = %q, exists=%t, %v", data, exists, err)
	}
	if store.values[entry.ID] != "JBSWY3DPEHPK3PXP" {
		t.Fatal("secret was not stored in secure-store seam")
	}
}

func TestVaultGeneratesRFC6238Code(t *testing.T) {
	store := &memoryStore{}
	vault, err := NewWithStore(t.TempDir(), store, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := vault.Add(ImportInput{Secret: "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", Label: "RFC", Algorithm: "SHA1", Digits: 8, Period: 30})
	if err != nil {
		t.Fatal(err)
	}
	code, err := vault.Generate(entry.ID, time.Unix(59, 0))
	if err != nil {
		t.Fatal(err)
	}
	if code.Value != "94287082" {
		t.Fatalf("code = %q", code.Value)
	}
	if !code.ValidFrom.Equal(time.Unix(30, 0).UTC()) || !code.ValidUntil.Equal(time.Unix(60, 0).UTC()) {
		t.Fatalf("window = %#v", code)
	}
}

func TestVaultRejectsInvalidURIAndKeepsIndexClean(t *testing.T) {
	vault, err := NewWithStore(t.TempDir(), &memoryStore{}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := vault.Add(ImportInput{URI: "otpauth://hotp/example?secret=JBSWY3DPEHPK3PXP"}); err == nil {
		t.Fatal("HOTP URI accepted")
	}
	entries, err := vault.List()
	if err != nil || len(entries) != 0 {
		t.Fatalf("entries = %#v, %v", entries, err)
	}
}

func TestVaultDeleteRestoresMetadataWhenSecretDeletionFails(t *testing.T) {
	store := &memoryStore{}
	vault, err := NewWithStore(t.TempDir(), store, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := vault.Add(ImportInput{Secret: "JBSWY3DPEHPK3PXP", Label: "test"})
	if err != nil {
		t.Fatal(err)
	}
	store.deleteErr = errors.New("credential store unavailable")
	if err := vault.Delete(entry.ID); err == nil {
		t.Fatal("delete unexpectedly succeeded")
	}
	entries, err := vault.List()
	if err != nil || len(entries) != 1 || entries[0].ID != entry.ID {
		t.Fatalf("metadata was not restored: %#v, %v", entries, err)
	}
}

func TestVaultEncryptedExportDoesNotExposeSecretInCleartext(t *testing.T) {
	store := &memoryStore{}
	vault, err := NewWithStore(t.TempDir(), store, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := vault.Add(ImportInput{Secret: "JBSWY3DPEHPK3PXP", Label: "export"}); err != nil {
		t.Fatal(err)
	}
	data, err := vault.ExportEncrypted("long-enough-export-password")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "JBSWY3DPEHPK3PXP") || !strings.Contains(string(data), "ciphertext") {
		t.Fatalf("encrypted export = %s", data)
	}
}

func TestVaultEncryptedExportRoundTripsIntoFreshCredentialStore(t *testing.T) {
	sourceStore := &memoryStore{}
	source, err := NewWithStore(t.TempDir(), sourceStore, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	sourceEntry, err := source.Add(ImportInput{Secret: "JBSWY3DPEHPK3PXP", Label: "XIASS API", Issuer: "XIASS", Account: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := source.ExportEncrypted("long-enough-export-password")
	if err != nil {
		t.Fatal(err)
	}

	targetStore := &memoryStore{}
	target, err := NewWithStore(t.TempDir(), targetStore, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	imported, err := target.ImportEncrypted(payload, "long-enough-export-password")
	if err != nil {
		t.Fatal(err)
	}
	if len(imported) != 1 || imported[0].ID == sourceEntry.ID || imported[0].Label != sourceEntry.Label {
		t.Fatalf("imported entries = %#v", imported)
	}
	entries, err := target.List()
	if err != nil || len(entries) != 1 || entries[0].ID != imported[0].ID {
		t.Fatalf("target entries = %#v, %v", entries, err)
	}
	if targetStore.values[imported[0].ID] != "JBSWY3DPEHPK3PXP" {
		t.Fatal("imported secret was not written to the target credential store")
	}
	if _, err := target.Generate(imported[0].ID, time.Now()); err != nil {
		t.Fatalf("imported entry cannot generate a code: %v", err)
	}
}

func TestVaultEncryptedImportRejectsWrongPasswordWithoutChanges(t *testing.T) {
	source, err := NewWithStore(t.TempDir(), &memoryStore{}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Add(ImportInput{Secret: "JBSWY3DPEHPK3PXP", Label: "XIASS"}); err != nil {
		t.Fatal(err)
	}
	payload, err := source.ExportEncrypted("long-enough-export-password")
	if err != nil {
		t.Fatal(err)
	}
	targetStore := &memoryStore{}
	target, err := NewWithStore(t.TempDir(), targetStore, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := target.ImportEncrypted(payload, "different-export-password"); err == nil {
		t.Fatal("wrong password unexpectedly imported a backup")
	}
	entries, err := target.List()
	if err != nil || len(entries) != 0 || len(targetStore.values) != 0 {
		t.Fatalf("wrong password changed target state: entries=%#v, store=%#v, err=%v", entries, targetStore.values, err)
	}
}

func TestVaultEncryptedImportRejectsDuplicateWithoutChanges(t *testing.T) {
	source, err := NewWithStore(t.TempDir(), &memoryStore{}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Add(ImportInput{Secret: "JBSWY3DPEHPK3PXP", Label: "XIASS", Issuer: "XIASS", Account: "alice"}); err != nil {
		t.Fatal(err)
	}
	payload, err := source.ExportEncrypted("long-enough-export-password")
	if err != nil {
		t.Fatal(err)
	}
	targetStore := &memoryStore{}
	target, err := NewWithStore(t.TempDir(), targetStore, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := target.Add(ImportInput{Secret: "JBSWY3DPEHPK3PXP", Label: "XIASS", Issuer: "XIASS", Account: "alice"}); err != nil {
		t.Fatal(err)
	}
	if _, err := target.ImportEncrypted(payload, "long-enough-export-password"); err == nil {
		t.Fatal("duplicate import unexpectedly succeeded")
	}
	entries, err := target.List()
	if err != nil || len(entries) != 1 || len(targetStore.values) != 1 {
		t.Fatalf("duplicate import changed target state: entries=%#v, store=%#v, err=%v", entries, targetStore.values, err)
	}
}

func TestVaultEncryptedImportRollsBackStoredSecretsAfterWriteFailure(t *testing.T) {
	source, err := NewWithStore(t.TempDir(), &memoryStore{}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []ImportInput{
		{Secret: "JBSWY3DPEHPK3PXP", Label: "XIASS A"},
		{Secret: "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", Label: "XIASS B"},
	} {
		if _, err := source.Add(input); err != nil {
			t.Fatal(err)
		}
	}
	payload, err := source.ExportEncrypted("long-enough-export-password")
	if err != nil {
		t.Fatal(err)
	}
	targetStore := &failAfterSetStore{failAt: 2}
	target, err := NewWithStore(t.TempDir(), targetStore, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := target.ImportEncrypted(payload, "long-enough-export-password"); err == nil {
		t.Fatal("credential-store failure unexpectedly succeeded")
	}
	entries, err := target.List()
	if err != nil || len(entries) != 0 || len(targetStore.values) != 0 {
		t.Fatalf("failed import was not rolled back: entries=%#v, store=%#v, err=%v", entries, targetStore.values, err)
	}
}

func TestVaultEncryptedImportRecoversDeferredCredentialCleanup(t *testing.T) {
	source, err := NewWithStore(t.TempDir(), &memoryStore{}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []ImportInput{
		{Secret: "JBSWY3DPEHPK3PXP", Label: "XIASS A"},
		{Secret: "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", Label: "XIASS B"},
	} {
		if _, err := source.Add(input); err != nil {
			t.Fatal(err)
		}
	}
	payload, err := source.ExportEncrypted("long-enough-export-password")
	if err != nil {
		t.Fatal(err)
	}
	targetStore := &cleanupRecoveryStore{failAt: 2, failDeletion: true}
	target, err := NewWithStore(t.TempDir(), targetStore, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := target.ImportEncrypted(payload, "long-enough-export-password"); err == nil {
		t.Fatal("credential-store failure unexpectedly succeeded")
	}
	if _, err := os.Stat(target.cleanup); err != nil {
		t.Fatalf("missing non-secret cleanup journal after failed cleanup: %v", err)
	}
	if len(targetStore.values) != 1 {
		t.Fatalf("want one temporary credential before recovery, got %#v", targetStore.values)
	}
	targetStore.failDeletion = false
	entries, err := target.List()
	if err != nil || len(entries) != 0 || len(targetStore.values) != 0 {
		t.Fatalf("deferred credential cleanup was not recovered: entries=%#v, store=%#v, err=%v", entries, targetStore.values, err)
	}
	if _, err := os.Stat(target.cleanup); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cleanup journal remained after recovery: %v", err)
	}
}

func TestVaultEncryptedExportAndImportSupportExactEntryLimit(t *testing.T) {
	store := &memoryStore{}
	source, err := NewWithStore(t.TempDir(), store, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	store.values = map[string]string{}
	document := indexDocument{Version: indexVersion, Entries: make([]Entry, 0, maxTOTPEntries)}
	stamp := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	for index := 0; index < maxTOTPEntries; index++ {
		id := fmt.Sprintf("totp-%032x", index+1)
		document.Entries = append(document.Entries, Entry{
			ID: id, Label: fmt.Sprintf("Entry %04d", index), Algorithm: "SHA1", Digits: 6, Period: 30,
			CreatedAt: stamp, UpdatedAt: stamp,
		})
		store.values[id] = "JBSWY3DPEHPK3PXP"
	}
	if err := source.saveLocked(document); err != nil {
		t.Fatal(err)
	}
	payload, err := source.ExportEncrypted("long-enough-export-password")
	if err != nil {
		t.Fatalf("exact-limit export failed: %v", err)
	}
	targetStore := &memoryStore{}
	target, err := NewWithStore(t.TempDir(), targetStore, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	imported, err := target.ImportEncrypted(payload, "long-enough-export-password")
	if err != nil || len(imported) != maxTOTPEntries || len(targetStore.values) != maxTOTPEntries {
		t.Fatalf("exact-limit backup did not fully restore: imported=%d, stored=%d, err=%v", len(imported), len(targetStore.values), err)
	}
	if _, err := target.Add(ImportInput{Secret: "JBSWY3DPEHPK3PXP", Label: "Over capacity"}); err == nil {
		t.Fatal("entry above the supported capacity was accepted")
	}
}
