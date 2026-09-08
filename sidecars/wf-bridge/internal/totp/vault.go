package totp

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	keyring "github.com/zalando/go-keyring"
)

// Vault keeps public labels in a private metadata file and the actual Base32
// secret exclusively in the platform credential store.
type Vault struct {
	mu      sync.Mutex
	index   string
	cleanup string
	secrets SecretStore
	now     func() time.Time
}

// New creates the normal platform-backed vault under the application's
// private state directory. It does not contact a network service.
func New(dataDir string) (*Vault, error) {
	return NewWithStore(dataDir, keyringStore{}, time.Now)
}

// NewWithStore is primarily useful for deterministic tests and platform
// integrations. It retains the same private metadata format as New.
func NewWithStore(dataDir string, secrets SecretStore, now func() time.Time) (*Vault, error) {
	dataDir = strings.TrimSpace(dataDir)
	if dataDir == "" {
		return nil, errors.New("TOTP data directory is empty")
	}
	if secrets == nil {
		return nil, errors.New("secure credential store is unavailable")
	}
	if now == nil {
		now = time.Now
	}
	base := filepath.Clean(dataDir)
	return &Vault{
		index:   filepath.Join(base, indexFileName),
		cleanup: filepath.Join(base, cleanupFileName),
		secrets: secrets,
		now:     now,
	}, nil
}

func (vault *Vault) List() ([]Entry, error) {
	if vault == nil {
		return nil, errors.New("TOTP vault is unavailable")
	}
	vault.mu.Lock()
	defer vault.mu.Unlock()
	document, err := vault.loadLocked()
	if err != nil {
		return nil, err
	}
	if err := vault.recoverPendingCleanupLocked(document); err != nil {
		return nil, err
	}
	entries := append([]Entry(nil), document.Entries...)
	sort.Slice(entries, func(i, j int) bool {
		left := strings.ToLower(entries[i].Label + "\x00" + entries[i].ID)
		right := strings.ToLower(entries[j].Label + "\x00" + entries[j].ID)
		return left < right
	})
	return entries, nil
}

// Add validates the supplied secret before it enters any storage. The secret
// is first written to Keychain/Credential Manager, then metadata is committed
// atomically; an index failure removes the just-created keyring item.
func (vault *Vault) Add(input ImportInput) (Entry, error) {
	if vault == nil {
		return Entry{}, errors.New("TOTP vault is unavailable")
	}
	entry, secret, err := normalizeImport(input, vault.now().UTC())
	if err != nil {
		return Entry{}, err
	}
	vault.mu.Lock()
	defer vault.mu.Unlock()
	document, err := vault.loadLocked()
	if err != nil {
		return Entry{}, err
	}
	if err := vault.recoverPendingCleanupLocked(document); err != nil {
		return Entry{}, err
	}
	if len(document.Entries) >= maxTOTPEntries {
		return Entry{}, fmt.Errorf("TOTP vault supports at most %d entries", maxTOTPEntries)
	}
	entry.ID, err = newID()
	if err != nil {
		return Entry{}, err
	}
	if err := vault.recordPendingCleanupLocked([]string{entry.ID}); err != nil {
		return Entry{}, err
	}
	if err := vault.secrets.Set(ServiceName, entry.ID, secret); err != nil {
		if cleanupErr := vault.abortPendingCleanupLocked([]string{entry.ID}); cleanupErr != nil {
			return Entry{}, errors.New("could not store the TOTP secret; credential cleanup will resume automatically")
		}
		return Entry{}, fmt.Errorf("store the TOTP secret in the system credential vault: %w", err)
	}
	document.Entries = append(document.Entries, entry)
	if err := vault.saveLocked(document); err != nil {
		if cleanupErr := vault.abortPendingCleanupLocked([]string{entry.ID}); cleanupErr != nil {
			return Entry{}, errors.New("could not save TOTP metadata; credential cleanup will resume automatically")
		}
		return Entry{}, fmt.Errorf("save TOTP metadata: %w", err)
	}
	// The metadata is now the source of truth. A failed journal removal is safe:
	// the next vault operation sees the live ID and removes the journal without
	// touching its secret.
	_ = vault.clearPendingCleanupLocked()
	return entry, nil
}

func (vault *Vault) Generate(id string, at time.Time) (Code, error) {
	if vault == nil {
		return Code{}, errors.New("TOTP vault is unavailable")
	}
	entry, secret, err := vault.entryAndSecret(id)
	if err != nil {
		return Code{}, err
	}
	return generate(entry, secret, at)
}

// Delete removes metadata and secure material together. If credential-store
// deletion fails, it restores the original metadata so the user is never left
// with an invisible secret they cannot manage.
func (vault *Vault) Delete(id string) error {
	if vault == nil {
		return errors.New("TOTP vault is unavailable")
	}
	id = strings.TrimSpace(id)
	if !validID(id) {
		return errors.New("invalid TOTP entry ID")
	}
	vault.mu.Lock()
	defer vault.mu.Unlock()
	document, err := vault.loadLocked()
	if err != nil {
		return err
	}
	if err := vault.recoverPendingCleanupLocked(document); err != nil {
		return err
	}
	index := findEntry(document.Entries, id)
	if index < 0 {
		return errors.New("TOTP entry does not exist")
	}
	original := document
	document.Entries = append(append([]Entry(nil), document.Entries[:index]...), document.Entries[index+1:]...)
	if err := vault.saveLocked(document); err != nil {
		return err
	}
	if err := vault.secrets.Delete(ServiceName, id); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		if restoreErr := vault.saveLocked(original); restoreErr != nil {
			return fmt.Errorf("remove TOTP secret failed (%v), and metadata restore failed (%v)", err, restoreErr)
		}
		return fmt.Errorf("remove TOTP secret from the system credential vault: %w", err)
	}
	return nil
}

func (vault *Vault) entryAndSecret(id string) (Entry, string, error) {
	id = strings.TrimSpace(id)
	if !validID(id) {
		return Entry{}, "", errors.New("invalid TOTP entry ID")
	}
	vault.mu.Lock()
	defer vault.mu.Unlock()
	document, err := vault.loadLocked()
	if err != nil {
		return Entry{}, "", err
	}
	if err := vault.recoverPendingCleanupLocked(document); err != nil {
		return Entry{}, "", err
	}
	index := findEntry(document.Entries, id)
	if index < 0 {
		return Entry{}, "", errors.New("TOTP entry does not exist")
	}
	secret, err := vault.secrets.Get(ServiceName, id)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return Entry{}, "", errors.New("TOTP secret is missing from the system credential vault")
		}
		return Entry{}, "", fmt.Errorf("read the TOTP secret from the system credential vault: %w", err)
	}
	return document.Entries[index], secret, nil
}

func (vault *Vault) loadLocked() (indexDocument, error) {
	data, exists, err := readRegularFile(vault.index)
	if err != nil {
		return indexDocument{}, err
	}
	if !exists {
		return indexDocument{Version: indexVersion, Entries: []Entry{}}, nil
	}
	var document indexDocument
	if err := json.Unmarshal(data, &document); err != nil {
		return indexDocument{}, errors.New("TOTP metadata is invalid")
	}
	if document.Version != indexVersion {
		return indexDocument{}, errors.New("TOTP metadata version is unsupported")
	}
	seen := map[string]struct{}{}
	for _, entry := range document.Entries {
		if err := validateEntry(entry); err != nil {
			return indexDocument{}, fmt.Errorf("TOTP metadata contains an invalid entry: %w", err)
		}
		if _, duplicate := seen[entry.ID]; duplicate {
			return indexDocument{}, errors.New("TOTP metadata contains duplicate IDs")
		}
		seen[entry.ID] = struct{}{}
	}
	return document, nil
}

func (vault *Vault) saveLocked(document indexDocument) error {
	document.Version = indexVersion
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(vault.index, append(data, '\n'))
}

func findEntry(entries []Entry, id string) int {
	for index, entry := range entries {
		if entry.ID == id {
			return index
		}
	}
	return -1
}

func newID() (string, error) {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return "totp-" + hex.EncodeToString(buffer), nil
}

func validID(id string) bool {
	if !strings.HasPrefix(id, "totp-") || len(id) != len("totp-")+32 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(id, "totp-"))
	return err == nil
}

func readRegularFile(path string) ([]byte, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, false, errors.New("TOTP metadata is not a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

func atomicWrite(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".xiass-totp-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	keep := false
	defer func() {
		_ = temporary.Close()
		if !keep {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	keep = true
	return nil
}

type keyringStore struct{}

func (keyringStore) Set(service, account, secret string) error {
	return keyring.Set(service, account, secret)
}
func (keyringStore) Get(service, account string) (string, error) {
	return keyring.Get(service, account)
}
func (keyringStore) Delete(service, account string) error { return keyring.Delete(service, account) }
