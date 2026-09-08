// Package totp manages RFC 6238 time-based one-time-password secrets without
// placing the secret material in the application JSON state or WebView.
package totp

import "time"

const (
	ServiceName      = "com.xiass.tools.totp"
	indexFileName    = "totp-index.json"
	cleanupFileName  = "totp-pending-cleanup.json"
	indexVersion     = 1
	exportVersion    = 1
	cleanupVersion   = 1
	maxTOTPEntries   = 1024
	defaultAlgorithm = "SHA1"
	defaultDigits    = 6
	defaultPeriod    = 30
)

// Entry is deliberately metadata-only. Secret is never represented by this
// public type and is stored in the operating system credential vault.
type Entry struct {
	ID        string    `json:"id"`
	Label     string    `json:"label"`
	Issuer    string    `json:"issuer,omitempty"`
	Account   string    `json:"account,omitempty"`
	Algorithm string    `json:"algorithm"`
	Digits    int       `json:"digits"`
	Period    int       `json:"period"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// ImportInput supports manual Base32 entry or standard otpauth://totp URIs.
// When URI is present it is the primary source; optional explicit label and
// issuer fields may refine its display metadata but never the secret itself.
type ImportInput struct {
	URI       string `json:"uri,omitempty"`
	Secret    string `json:"secret,omitempty"`
	Label     string `json:"label,omitempty"`
	Issuer    string `json:"issuer,omitempty"`
	Account   string `json:"account,omitempty"`
	Algorithm string `json:"algorithm,omitempty"`
	Digits    int    `json:"digits,omitempty"`
	Period    int    `json:"period,omitempty"`
}

// Code is a short-lived generated TOTP value. It is intentionally not
// persisted, logged, or included in diagnostic exports.
type Code struct {
	EntryID    string    `json:"entryId"`
	Value      string    `json:"value"`
	ValidFrom  time.Time `json:"validFrom"`
	ValidUntil time.Time `json:"validUntil"`
}

// SecretStore maps a generated entry ID to a platform-secure secret. The
// default uses macOS Keychain or Windows Credential Manager via go-keyring;
// tests inject an in-memory implementation.
type SecretStore interface {
	Set(service, account, secret string) error
	Get(service, account string) (string, error)
	Delete(service, account string) error
}

type indexDocument struct {
	Version int     `json:"version"`
	Entries []Entry `json:"entries"`
}

type exportDocument struct {
	Version int           `json:"version"`
	Entries []exportEntry `json:"entries"`
}

type exportEntry struct {
	Entry
	Secret string `json:"secret"`
}

type encryptedExport struct {
	Version    int    `json:"version"`
	KDF        string `json:"kdf"`
	Salt       string `json:"salt"`
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ciphertext"`
}

// pendingCleanupDocument records only generated credential-vault IDs. It is
// written before a multi-store change so an interrupted import can be resumed
// safely on the next vault operation. It never contains a TOTP secret.
type pendingCleanupDocument struct {
	Version int      `json:"version"`
	IDs     []string `json:"ids"`
}
