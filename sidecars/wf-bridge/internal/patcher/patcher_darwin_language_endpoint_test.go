//go:build darwin

package patcher

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestDarwinLanguagePatchSupportsEveryKnownCloudCodeHostAtFixedLength(t *testing.T) {
	restoreEndpoint := setPatchProxyPortForTest(51042)
	t.Cleanup(restoreEndpoint)

	path := filepath.Join(t.TempDir(), "language_server_macos_x64")
	originals := []string{
		productionEndpoint,
		sandboxEndpoint,
		publicEndpoint,
		autopushEndpoint,
	}
	original := []byte("head\x00" + originals[0] + "\x00" + originals[1] + "\x00" + originals[2] + "\x00" + originals[3] + "\x00tail")
	if err := os.WriteFile(path, original, 0o755); err != nil {
		t.Fatal(err)
	}

	plan, embedded, err := prepareDarwinLanguagePatch(path)
	if err != nil {
		t.Fatal(err)
	}
	if !embedded || !plan.changed {
		t.Fatalf("known Cloud Code endpoints were not detected: %+v", plan)
	}
	if len(plan.updated) != len(original) {
		t.Fatalf("fixed-size binary patch changed length: got %d, want %d", len(plan.updated), len(original))
	}
	for _, originalURL := range originals {
		replacement, err := darwinBinaryEndpointFor(originalURL)
		if err != nil {
			t.Fatal(err)
		}
		if len(replacement) != len(originalURL) {
			t.Fatalf("replacement length for %q = %d, want %d", originalURL, len(replacement), len(originalURL))
		}
		if bytes.Contains(plan.updated, []byte(originalURL)) {
			t.Fatalf("original endpoint remained in binary: %q", originalURL)
		}
		if !bytes.Contains(plan.updated, []byte(replacement)) {
			t.Fatalf("replacement missing for %q: %q", originalURL, replacement)
		}
	}
	if err := os.WriteFile(path, plan.updated, 0o755); err != nil {
		t.Fatal(err)
	}
	if patched, embedded := darwinLanguagePatchState(path); !patched || !embedded {
		t.Fatalf("generic endpoint patch state = patched:%t embedded:%t", patched, embedded)
	}
}

func TestDarwinLanguagePatchLeavesNonGoogleURLsUntouched(t *testing.T) {
	path := filepath.Join(t.TempDir(), "language_server_macos_x64")
	original := []byte("prefix https://api.example.test/v1 https://cloudcode-pa.example.com suffix")
	if err := os.WriteFile(path, original, 0o755); err != nil {
		t.Fatal(err)
	}
	plan, embedded, err := prepareDarwinLanguagePatch(path)
	if err != nil {
		t.Fatal(err)
	}
	if embedded || plan.changed {
		t.Fatalf("non-Google URL was treated as a patch target: embedded=%t plan=%+v", embedded, plan)
	}
	if !bytes.Equal(plan.original, original) || !bytes.Equal(plan.updated, original) {
		t.Fatalf("non-Google URL was rewritten: got %q, want %q", plan.updated, original)
	}
	if patched, embedded := darwinLanguagePatchState(path); !patched || embedded {
		t.Fatalf("non-Google endpoint state = patched:%t embedded:%t", patched, embedded)
	}
}

func TestDarwinLanguagePatchStateRejectsStaleManagedEndpoint(t *testing.T) {
	restoreEndpoint := setPatchProxyPortForTest(51042)
	t.Cleanup(restoreEndpoint)
	path := filepath.Join(t.TempDir(), "language_server_macos_x64")
	stale := "http://127.0.0.1:50999/v1internal/xxxxxxxxxxxxxxxx"
	if err := os.WriteFile(path, []byte("prefix "+stale+" suffix"), 0o755); err != nil {
		t.Fatal(err)
	}
	if patched, embedded := darwinLanguagePatchState(path); patched || !embedded {
		t.Fatalf("stale managed endpoint state = patched:%t embedded:%t", patched, embedded)
	}
}

func TestDarwinPatchApplyAndRestoreGenericLanguageServerHost(t *testing.T) {
	appPath := filepath.Join(t.TempDir(), "Antigravity IDE.app")
	mainPath := filepath.Join(appPath, "Contents", "Resources", "app", "out", "main.js")
	extensionPath := filepath.Join(appPath, "Contents", "Resources", "app", "extensions", "antigravity", "dist", "extension.js")
	languagePath := filepath.Join(appPath, "Contents", "Resources", "app", "extensions", "antigravity", "bin", "language_server_macos_x64")
	for _, path := range []string{mainPath, extensionPath, languagePath} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	originalMain := []byte(`"use strict";const endpoint="` + productionEndpoint + `";` + authEligibilityOriginal)
	originalExtension := []byte(`const endpoint=await service.getCloudCodeUrl();`)
	originalLanguage := []byte("prefix\x00" + publicEndpoint + "\x00suffix")
	if err := os.WriteFile(mainPath, originalMain, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(extensionPath, originalExtension, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(languagePath, originalLanguage, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("ANTIGRAVITY_WF_BACKUP_DIR", t.TempDir())
	t.Setenv("ANTIGRAVITY_WF_SKIP_CODESIGN", "1")
	target := darwinTargets{
		app: appPath, name: "Antigravity IDE", kind: "ide",
		main: mainPath, extensionEntry: extensionPath, language: languagePath,
	}
	if _, err := applyDarwinPatch(target); err != nil {
		t.Fatal(err)
	}
	patchedLanguage, err := os.ReadFile(languagePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(patchedLanguage) != len(originalLanguage) {
		t.Fatalf("generic LS patch changed binary length: got %d, want %d", len(patchedLanguage), len(originalLanguage))
	}
	replacement, err := darwinBinaryEndpointFor(publicEndpoint)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(patchedLanguage, []byte(replacement)) || bytes.Contains(patchedLanguage, []byte(publicEndpoint)) {
		t.Fatalf("generic LS endpoint was not patched safely: %q", patchedLanguage)
	}
	if _, _, _, patched := darwinTargetPatchState(target); !patched {
		t.Fatal("generic LS integration fixture was not fully patched")
	}
	if _, err := restoreDarwinPatch(target); err != nil {
		t.Fatal(err)
	}
	assertFileEquals(t, mainPath, originalMain)
	assertFileEquals(t, extensionPath, originalExtension)
	assertFileEquals(t, languagePath, originalLanguage)
}
