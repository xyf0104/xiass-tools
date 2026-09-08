package totp

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	keyring "github.com/zalando/go-keyring"
	"golang.org/x/crypto/scrypt"
)

const (
	encryptedExportKDF      = "scrypt-N32768-r8-p1-AES-256-GCM"
	encryptedExportAAD      = "XIASS Tools TOTP export v1"
	maxEncryptedExportBytes = 8 * 1024 * 1024
)

// ExportEncrypted returns a password-encrypted portable backup. It is an
// explicitly requested operation: normal diagnostics and local metadata never
// include a secret or a generated code.
func (vault *Vault) ExportEncrypted(password string) ([]byte, error) {
	if vault == nil {
		return nil, errors.New("TOTP vault is unavailable")
	}
	password, err := normalizeExportPassword(password)
	if err != nil {
		return nil, err
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
	if len(document.Entries) > maxTOTPEntries {
		return nil, fmt.Errorf("TOTP vault exceeds the supported %d-entry export limit", maxTOTPEntries)
	}
	export := exportDocument{Version: exportVersion, Entries: make([]exportEntry, 0, len(document.Entries))}
	for _, entry := range document.Entries {
		secret, err := vault.secrets.Get(ServiceName, entry.ID)
		if err != nil {
			return nil, fmt.Errorf("read a TOTP secret for encrypted export: %w", err)
		}
		export.Entries = append(export.Entries, exportEntry{Entry: entry, Secret: secret})
	}
	plain, err := json.Marshal(export)
	if err != nil {
		return nil, err
	}
	defer clearBytes(plain)
	salt := make([]byte, 16)
	nonce := make([]byte, 12)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	key, err := scrypt.Key([]byte(password), salt, 32768, 8, 1, 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	ciphertext := gcm.Seal(nil, nonce, plain, []byte(encryptedExportAAD))
	encoded, err := json.MarshalIndent(encryptedExport{
		Version: exportVersion, KDF: encryptedExportKDF,
		Salt: base64.RawStdEncoding.EncodeToString(salt), Nonce: base64.RawStdEncoding.EncodeToString(nonce),
		Ciphertext: base64.RawStdEncoding.EncodeToString(ciphertext),
	}, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

// ImportEncrypted restores a portable XIASS Tools backup into the current
// system credential vault. It never overwrites current entries: any semantic
// duplicate rejects the whole import before secret material is written. New
// secrets are written first, followed by one atomic metadata update; a failed
// write removes every secret created by this import before returning.
func (vault *Vault) ImportEncrypted(data []byte, password string) ([]Entry, error) {
	if vault == nil {
		return nil, errors.New("TOTP vault is unavailable")
	}
	if len(data) == 0 || len(data) > maxEncryptedExportBytes {
		return nil, errors.New("encrypted TOTP export has an unsupported size")
	}
	password, err := normalizeExportPassword(password)
	if err != nil {
		return nil, err
	}
	backup, err := decryptEncryptedExport(data, password)
	if err != nil {
		return nil, err
	}
	if len(backup.Entries) == 0 {
		return nil, errors.New("encrypted TOTP export contains no entries")
	}
	if len(backup.Entries) > maxTOTPEntries {
		return nil, errors.New("encrypted TOTP export contains too many entries")
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
	if len(document.Entries) > maxTOTPEntries-len(backup.Entries) {
		return nil, errors.New("encrypted TOTP export would exceed the local entry limit")
	}

	usedIDs := make(map[string]struct{}, len(document.Entries)+len(backup.Entries))
	identities := make(map[string]struct{}, len(document.Entries)+len(backup.Entries))
	for _, entry := range document.Entries {
		usedIDs[entry.ID] = struct{}{}
		identities[entryIdentity(entry)] = struct{}{}
	}

	prepared := make([]exportEntry, 0, len(backup.Entries))
	sourceIDs := make(map[string]struct{}, len(backup.Entries))
	for _, exported := range backup.Entries {
		exported, err = normalizeExportEntry(exported)
		if err != nil {
			return nil, err
		}
		if _, exists := sourceIDs[exported.ID]; exists {
			return nil, errors.New("encrypted TOTP export contains duplicate entries")
		}
		sourceIDs[exported.ID] = struct{}{}
		identity := entryIdentity(exported.Entry)
		if _, exists := identities[identity]; exists {
			return nil, errors.New("an imported TOTP entry is already present")
		}
		identities[identity] = struct{}{}
		prepared = append(prepared, exported)
	}

	entries := make([]Entry, 0, len(prepared))
	for _, exported := range prepared {
		entry := exported.Entry
		for {
			entry.ID, err = newID()
			if err != nil {
				return nil, err
			}
			if _, exists := usedIDs[entry.ID]; !exists {
				usedIDs[entry.ID] = struct{}{}
				break
			}
		}
		entries = append(entries, entry)
	}

	cleanupIDs := make([]string, len(entries))
	for index, entry := range entries {
		cleanupIDs[index] = entry.ID
	}
	if err := vault.recordPendingCleanupLocked(cleanupIDs); err != nil {
		return nil, err
	}
	for index, entry := range entries {
		if err := vault.secrets.Set(ServiceName, entry.ID, prepared[index].Secret); err != nil {
			if cleanupErr := vault.abortPendingCleanupLocked(cleanupIDs); cleanupErr != nil {
				return nil, errors.New("could not store encrypted TOTP import; credential cleanup will resume automatically")
			}
			return nil, errors.New("could not store encrypted TOTP import")
		}
	}

	next := document
	next.Entries = append(append([]Entry(nil), document.Entries...), entries...)
	if err := vault.saveLocked(next); err != nil {
		if cleanupErr := vault.abortPendingCleanupLocked(cleanupIDs); cleanupErr != nil {
			return nil, errors.New("could not save imported TOTP metadata; credential cleanup will resume automatically")
		}
		return nil, errors.New("could not save imported TOTP metadata")
	}
	_ = vault.clearPendingCleanupLocked()
	return entries, nil
}

func normalizeExportPassword(password string) (string, error) {
	password = strings.TrimSpace(password)
	if len(password) < 10 || len(password) > 1024 {
		return "", errors.New("export password must contain at least 10 characters")
	}
	return password, nil
}

func decryptEncryptedExport(data []byte, password string) (exportDocument, error) {
	var envelope encryptedExport
	if err := decodeStrictJSON(data, &envelope); err != nil {
		return exportDocument{}, errors.New("encrypted TOTP export format is invalid")
	}
	if envelope.Version != exportVersion || envelope.KDF != encryptedExportKDF {
		return exportDocument{}, errors.New("encrypted TOTP export version is unsupported")
	}
	salt, err := base64.RawStdEncoding.DecodeString(envelope.Salt)
	if err != nil || len(salt) != 16 {
		return exportDocument{}, errors.New("encrypted TOTP export salt is invalid")
	}
	nonce, err := base64.RawStdEncoding.DecodeString(envelope.Nonce)
	if err != nil || len(nonce) != 12 {
		return exportDocument{}, errors.New("encrypted TOTP export nonce is invalid")
	}
	ciphertext, err := base64.RawStdEncoding.DecodeString(envelope.Ciphertext)
	if err != nil || len(ciphertext) < 16 {
		return exportDocument{}, errors.New("encrypted TOTP export ciphertext is invalid")
	}
	key, err := scrypt.Key([]byte(password), salt, 32768, 8, 1, 32)
	if err != nil {
		return exportDocument{}, errors.New("could not derive the TOTP export key")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return exportDocument{}, errors.New("could not open encrypted TOTP export")
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return exportDocument{}, errors.New("could not open encrypted TOTP export")
	}
	if len(ciphertext) < gcm.Overhead() {
		return exportDocument{}, errors.New("encrypted TOTP export ciphertext is invalid")
	}
	plain, err := gcm.Open(nil, nonce, ciphertext, []byte(encryptedExportAAD))
	if err != nil {
		return exportDocument{}, errors.New("could not decrypt encrypted TOTP export")
	}
	defer clearBytes(plain)
	var backup exportDocument
	if err := decodeStrictJSON(plain, &backup); err != nil {
		return exportDocument{}, errors.New("decrypted TOTP export is invalid")
	}
	if backup.Version != exportVersion {
		return exportDocument{}, errors.New("decrypted TOTP export version is unsupported")
	}
	return backup, nil
}

func decodeStrictJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON data")
	}
	return nil
}

func normalizeExportEntry(exported exportEntry) (exportEntry, error) {
	if !validID(exported.ID) || exported.CreatedAt.IsZero() || exported.UpdatedAt.IsZero() {
		return exportEntry{}, errors.New("encrypted TOTP export contains an invalid entry")
	}
	if err := validateEntry(exported.Entry); err != nil {
		return exportEntry{}, errors.New("encrypted TOTP export contains an invalid entry")
	}
	secret, err := normalizeSecret(exported.Secret)
	if err != nil {
		return exportEntry{}, errors.New("encrypted TOTP export contains an invalid secret")
	}
	exported.Secret = secret
	return exported, nil
}

func entryIdentity(entry Entry) string {
	return strings.Join([]string{
		strings.ToLower(strings.TrimSpace(entry.Label)),
		strings.ToLower(strings.TrimSpace(entry.Issuer)),
		strings.ToLower(strings.TrimSpace(entry.Account)),
		strings.ToUpper(strings.TrimSpace(entry.Algorithm)),
		fmt.Sprintf("%d", entry.Digits),
		fmt.Sprintf("%d", entry.Period),
	}, "\x00")
}

func (vault *Vault) deleteImportedSecretsLocked(ids []string) error {
	var firstErr error
	for index := len(ids) - 1; index >= 0; index-- {
		if err := vault.secrets.Delete(ServiceName, ids[index]); err != nil && !errors.Is(err, keyring.ErrNotFound) {
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// recordPendingCleanupLocked journals a set of newly allocated credential IDs
// before touching the system credential vault. If a later metadata commit or
// cleanup fails, the next vault operation can safely finish the cleanup.
func (vault *Vault) recordPendingCleanupLocked(ids []string) error {
	if vault == nil || len(ids) == 0 {
		return errors.New("TOTP cleanup journal is invalid")
	}
	if _, exists, err := vault.loadPendingCleanupLocked(); err != nil {
		return err
	} else if exists {
		return errors.New("a previous TOTP credential cleanup is still pending")
	}
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if !validID(id) {
			return errors.New("TOTP cleanup journal is invalid")
		}
		if _, duplicate := seen[id]; duplicate {
			return errors.New("TOTP cleanup journal contains duplicate IDs")
		}
		seen[id] = struct{}{}
	}
	data, err := json.Marshal(pendingCleanupDocument{Version: cleanupVersion, IDs: append([]string(nil), ids...)})
	if err != nil {
		return err
	}
	return atomicWrite(vault.cleanup, append(data, '\n'))
}

func (vault *Vault) loadPendingCleanupLocked() (pendingCleanupDocument, bool, error) {
	data, exists, err := readRegularFile(vault.cleanup)
	if err != nil || !exists {
		return pendingCleanupDocument{}, exists, err
	}
	var journal pendingCleanupDocument
	if err := decodeStrictJSON(data, &journal); err != nil || journal.Version != cleanupVersion || len(journal.IDs) == 0 {
		return pendingCleanupDocument{}, false, errors.New("TOTP cleanup journal is invalid")
	}
	seen := make(map[string]struct{}, len(journal.IDs))
	for _, id := range journal.IDs {
		if !validID(id) {
			return pendingCleanupDocument{}, false, errors.New("TOTP cleanup journal is invalid")
		}
		if _, duplicate := seen[id]; duplicate {
			return pendingCleanupDocument{}, false, errors.New("TOTP cleanup journal is invalid")
		}
		seen[id] = struct{}{}
	}
	return journal, true, nil
}

func (vault *Vault) clearPendingCleanupLocked() error {
	err := os.Remove(vault.cleanup)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// recoverPendingCleanupLocked never deletes a credential ID that is already
// present in committed metadata. This makes an interrupted post-commit journal
// removal benign while still cleaning up secrets from an aborted transaction.
func (vault *Vault) recoverPendingCleanupLocked(document indexDocument) error {
	journal, exists, err := vault.loadPendingCleanupLocked()
	if err != nil || !exists {
		return err
	}
	live := make(map[string]struct{}, len(document.Entries))
	for _, entry := range document.Entries {
		live[entry.ID] = struct{}{}
	}
	for _, id := range journal.IDs {
		if _, committed := live[id]; committed {
			continue
		}
		if err := vault.secrets.Delete(ServiceName, id); err != nil && !errors.Is(err, keyring.ErrNotFound) {
			return errors.New("TOTP credential cleanup is pending; retry after the system credential vault is available")
		}
	}
	// A failed removal leaves a harmless journal. A later operation will retry
	// the same idempotent cleanup instead of losing the recovery record.
	_ = vault.clearPendingCleanupLocked()
	return nil
}

func (vault *Vault) abortPendingCleanupLocked(ids []string) error {
	if err := vault.deleteImportedSecretsLocked(ids); err != nil {
		return err
	}
	_ = vault.clearPendingCleanupLocked()
	return nil
}

func clearBytes(data []byte) {
	for index := range data {
		data[index] = 0
	}
}
