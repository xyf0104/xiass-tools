//go:build darwin

package patcher

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDarwinUnpackedIDENoLanguageServerUsesVerifiedEntrypoints(t *testing.T) {
	appPath := filepath.Join(t.TempDir(), "Antigravity IDE.app")
	appRoot := filepath.Join(appPath, "Contents", "Resources", "app")
	mainPath := filepath.Join(appRoot, "out", "main.js")
	extensionPath := filepath.Join(appRoot, "extensions", "antigravity", "dist", "extension.js")
	originalMain := []byte(`"use strict";const endpoint="` + productionEndpoint + `";` + authEligibilityOriginal)
	originalExtension := []byte(`const endpoint=await service.getCloudCodeUrl();`)
	for _, path := range []string{mainPath, extensionPath} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(mainPath, originalMain, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(extensionPath, originalExtension, 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("ANTIGRAVITY_APP_PATH", appPath)
	t.Setenv("ANTIGRAVITY_APP_PATHS", "")
	t.Setenv("ANTIGRAVITY_WF_BACKUP_DIR", t.TempDir())
	t.Setenv("ANTIGRAVITY_WF_SKIP_CODESIGN", "1")
	targets := locateDarwinInstallations()
	if len(targets) != 1 || targets[0].kind != "ide" || targets[0].language != "" {
		t.Fatalf("unpacked no-LS fixture was not safely discovered: %+v", targets)
	}
	message, err := applyDarwinPatch(targets[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(message, "没有独立 Language Server") {
		t.Fatalf("missing explicit no-LS warning: %s", message)
	}
	if _, _, _, patched := darwinTargetPatchState(targets[0]); !patched {
		t.Fatal("verified unpacked no-LS fixture was not fully patched")
	}
	if data, err := os.ReadFile(mainPath); err != nil || !bytes.Contains(data, []byte(currentPatchProxyEndpoint().Text)) {
		t.Fatalf("main entry was not patched: %v", err)
	}
	if data, err := os.ReadFile(extensionPath); err != nil || !bytes.Contains(data, []byte(darwinExtensionMarker)) {
		t.Fatalf("extension entry was not patched: %v", err)
	}
	if _, err := restoreDarwinPatch(targets[0]); err != nil {
		t.Fatal(err)
	}
	assertFileEquals(t, mainPath, originalMain)
	assertFileEquals(t, extensionPath, originalExtension)
}

func TestDarwinASARNoLanguageServerUsesVerifiedEntrypoints(t *testing.T) {
	appPath := filepath.Join(t.TempDir(), "Antigravity 2.0.app")
	asarPath := filepath.Join(appPath, "Contents", "Resources", "app.asar")
	if err := os.MkdirAll(filepath.Dir(asarPath), 0o755); err != nil {
		t.Fatal(err)
	}
	fixture := &asarArchive{root: &asarNode{Files: map[string]*asarNode{}}}
	if err := fixture.write(asarPath, map[string][]byte{
		"dist/main.js":           []byte(`"use strict";const endpoint="` + productionEndpoint + `";`),
		"dist/languageServer.js": []byte(`args.push("--cloud_code_endpoint","` + publicEndpoint + `");`),
	}); err != nil {
		t.Fatal(err)
	}
	originalPlist := writeDarwinAgentIntegrityFixture(t, appPath, asarPath)
	originalASAR, err := os.ReadFile(asarPath)
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv("ANTIGRAVITY_APP_PATH", appPath)
	t.Setenv("ANTIGRAVITY_APP_PATHS", "")
	t.Setenv("ANTIGRAVITY_WF_BACKUP_DIR", t.TempDir())
	t.Setenv("ANTIGRAVITY_WF_SKIP_CODESIGN", "1")
	targets := locateDarwinInstallations()
	if len(targets) != 1 || targets[0].kind != "agent" || targets[0].language != "" {
		t.Fatalf("ASAR no-LS fixture was not safely discovered: %+v", targets)
	}
	message, err := applyDarwinPatch(targets[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(message, "app.asar 中受验证的启动脚本") {
		t.Fatalf("missing explicit ASAR no-LS warning: %s", message)
	}
	if !darwinASARPatched(asarPath) {
		t.Fatal("ASAR launcher was not patched")
	}
	if _, _, _, patched := darwinTargetPatchState(targets[0]); !patched {
		t.Fatal("verified ASAR no-LS fixture was not fully patched")
	}
	if _, err := restoreDarwinPatch(targets[0]); err != nil {
		t.Fatal(err)
	}
	assertFileEquals(t, asarPath, originalASAR)
	assertFileEquals(t, filepath.Join(appPath, "Contents", "Info.plist"), originalPlist)
}

func TestDarwinNoLanguageServerUnknownLayoutsAreNotDiscovered(t *testing.T) {
	t.Run("unpacked IDE without extension", func(t *testing.T) {
		appPath := filepath.Join(t.TempDir(), "Antigravity IDE.app")
		mainPath := filepath.Join(appPath, "Contents", "Resources", "app", "out", "main.js")
		if err := os.MkdirAll(filepath.Dir(mainPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(mainPath, []byte(`"use strict";const endpoint="`+productionEndpoint+`";`), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv("ANTIGRAVITY_APP_PATH", appPath)
		t.Setenv("ANTIGRAVITY_APP_PATHS", "")
		if targets := locateDarwinInstallations(); len(targets) != 0 {
			t.Fatalf("unverified no-LS IDE was unexpectedly discovered: %+v", targets)
		}
	})

	t.Run("ASAR without supported launcher", func(t *testing.T) {
		appPath := filepath.Join(t.TempDir(), "Antigravity 2.0.app")
		asarPath := filepath.Join(appPath, "Contents", "Resources", "app.asar")
		if err := os.MkdirAll(filepath.Dir(asarPath), 0o755); err != nil {
			t.Fatal(err)
		}
		fixture := &asarArchive{root: &asarNode{Files: map[string]*asarNode{}}}
		if err := fixture.write(asarPath, map[string][]byte{
			"dist/main.js":           []byte(`"use strict";const endpoint="` + productionEndpoint + `";`),
			"dist/languageServer.js": []byte(`const unrelatedLauncher=true;`),
		}); err != nil {
			t.Fatal(err)
		}
		t.Setenv("ANTIGRAVITY_APP_PATH", appPath)
		t.Setenv("ANTIGRAVITY_APP_PATHS", "")
		if targets := locateDarwinInstallations(); len(targets) != 0 {
			t.Fatalf("unverified no-LS ASAR was unexpectedly discovered: %+v", targets)
		}
	})
}
