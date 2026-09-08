package codexconfig

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestHistoryRepairRefusesBeforeChangingFilesWhenCodexIsRunning(t *testing.T) {
	home := t.TempDir()
	writeHistoryConfig(t, home, DefaultProviderID)
	session := writeHistoryRollout(t, home, "sessions/blocked.jsonl", "openai", "thread-blocked")
	database := filepath.Join(home, "state_5.sqlite")
	createHistoryDatabase(t, database, map[string]string{"thread-blocked": "openai"})
	before, err := os.ReadFile(session)
	if err != nil {
		t.Fatal(err)
	}

	manager := NewManagerWithOptions(home, ManagerOptions{
		HistoryWriteGuard: func() error { return ErrCodexHistoryWriteUnsafe },
	})
	_, err = NewHistoryRepairerWithManager(manager).RepairCurrentProvider()
	if !errors.Is(err, ErrCodexHistoryWriteUnsafe) {
		t.Fatalf("RepairCurrentProvider() error = %v, want ErrCodexHistoryWriteUnsafe", err)
	}
	after, err := os.ReadFile(session)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("history JSONL changed even though the guard rejected the operation")
	}
	assertHistoryDatabase(t, database, 1, "openai")
	if _, err := os.Stat(filepath.Join(home, toolDataDirectory, historyBackupDirName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("history backup directory exists after an initial safety rejection: %v", err)
	}
}

func TestHistoryRepairRechecksBeforeChangingJSONLAfterPlanning(t *testing.T) {
	home := t.TempDir()
	writeHistoryConfig(t, home, DefaultProviderID)
	session := writeHistoryRollout(t, home, "sessions/recheck.jsonl", "openai", "thread-recheck")
	database := filepath.Join(home, "state_5.sqlite")
	createHistoryDatabase(t, database, map[string]string{"thread-recheck": "openai"})
	before, err := os.ReadFile(session)
	if err != nil {
		t.Fatal(err)
	}

	checks := 0
	manager := NewManagerWithOptions(home, ManagerOptions{
		HistoryWriteGuard: func() error {
			checks++
			if checks >= 5 {
				return ErrCodexHistoryWriteUnsafe
			}
			return nil
		},
	})
	_, err = NewHistoryRepairerWithManager(manager).RepairCurrentProvider()
	if !errors.Is(err, ErrCodexHistoryWriteUnsafe) {
		t.Fatalf("RepairCurrentProvider() error = %v, want ErrCodexHistoryWriteUnsafe", err)
	}
	if checks < 5 {
		t.Fatalf("history repair only checked safety %d time(s), want a pre-write recheck", checks)
	}
	after, err := os.ReadFile(session)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("history JSONL changed after a late safety rejection")
	}
	assertHistoryDatabase(t, database, 1, "openai")
}

func TestWorkspaceRepairRefusesBeforeChangingStateWhenCodexIsRunning(t *testing.T) {
	home := t.TempDir()
	manager := NewManagerWithOptions(home, ManagerOptions{
		HistoryWriteGuard: func() error { return ErrCodexHistoryWriteUnsafe },
	})
	statePath := filepath.Join(home, ".codex-global-state.json")
	before := []byte(`{"active-workspace-roots":["\\Users\\example\\project"]}`)
	if err := os.WriteFile(statePath, before, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := manager.RepairWorkspaceStateForOS("darwin")
	if !errors.Is(err, ErrCodexHistoryWriteUnsafe) {
		t.Fatalf("RepairWorkspaceStateForOS() error = %v, want ErrCodexHistoryWriteUnsafe", err)
	}
	after, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("workspace state changed even though the guard rejected the operation")
	}
}
