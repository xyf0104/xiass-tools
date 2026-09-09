package totp

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRenamePreservesSecretAndCode(t *testing.T) {
	at := time.Unix(1234567890, 0)
	store := &memoryStore{}
	vault, err := NewWithStore(t.TempDir(), store, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	entry, err := vault.Add(ImportInput{Secret: "JBSWY3DPEHPK3PXP", Label: "Before"})
	if err != nil {
		t.Fatal(err)
	}
	before, err := vault.Generate(entry.ID, at)
	if err != nil {
		t.Fatal(err)
	}
	// A rename must not need access to the secret store at all.
	store.getErr = errors.New("read forbidden")
	store.setErr = errors.New("write forbidden")
	store.deleteErr = errors.New("delete forbidden")
	renamed, err := vault.Rename(entry.ID, " XIASS API ")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Label != "XIASS API" || renamed.ID != entry.ID || renamed.CreatedAt != entry.CreatedAt {
		t.Fatal("metadata changed unexpectedly")
	}
	store.getErr = nil
	after, err := vault.Generate(entry.ID, at)
	if err != nil || before != after {
		t.Fatalf("code changed after rename: %v", err)
	}
	for _, label := range []string{"", " ", "a\nb", "a\tb", strings.Repeat("x", 201)} {
		if _, err := vault.Rename(entry.ID, label); err == nil {
			t.Fatalf("accepted invalid label %q", label)
		}
	}
	listed, err := vault.List()
	if err != nil || len(listed) != 1 || listed[0].Label != "XIASS API" {
		t.Fatal("rename was not persisted")
	}
	if _, err := vault.Rename("invalid", "Name"); err == nil {
		t.Fatal("accepted invalid ID")
	}
}
