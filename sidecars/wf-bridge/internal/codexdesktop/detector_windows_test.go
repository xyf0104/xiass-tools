//go:build windows

package codexdesktop

import (
	"os"
	"path/filepath"
	"testing"
)

const windowsManifestFixture = `<?xml version="1.0" encoding="utf-8"?>
<Package xmlns="http://schemas.microsoft.com/appx/manifest/foundation/windows10">
  <Identity Name="OpenAI.Codex" Publisher="CN=OpenAI" Version="1.2.3.4" />
  <Applications>
    <Application Id="App" Executable="app\Codex.exe" EntryPoint="Windows.FullTrustApplication" />
  </Applications>
</Package>`

func TestInspectWindowsCandidateAcceptsVerifiedFlattenedPortableLayout(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AppxManifest.xml"), []byte(windowsManifestFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Codex.exe"), []byte("test"), 0o700); err != nil {
		t.Fatal(err)
	}

	installation, unavailable, invalid := inspectWindowsCandidate(
		systemFileSystem{},
		windowsCandidate{root: root, source: SourceLocalAppData},
		"",
	)
	if unavailable || invalid || installation == nil {
		t.Fatalf("portable inspection = %#v unavailable=%v invalid=%v", installation, unavailable, invalid)
	}
	if !sameWindowsPath(installation.executable, filepath.Join(root, "Codex.exe")) || installation.version != "1.2.3.4" {
		t.Fatalf("portable installation = %#v", installation)
	}
}

func TestInspectWindowsStoreCandidateDoesNotAcceptFlattenedExecutable(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AppxManifest.xml"), []byte(windowsManifestFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Codex.exe"), []byte("test"), 0o700); err != nil {
		t.Fatal(err)
	}

	installation, unavailable, invalid := inspectWindowsCandidate(
		systemFileSystem{},
		windowsCandidate{root: root, source: SourceWindowsStore},
		filepath.Join(root, "app", "Codex.exe"),
	)
	if installation != nil || unavailable || !invalid {
		t.Fatalf("store inspection trusted flattened layout: %#v unavailable=%v invalid=%v", installation, unavailable, invalid)
	}
}

func TestWindowsExecutableMatchingExcludesSameNamedCLIAtAnotherPath(t *testing.T) {
	root := t.TempDir()
	desktop := filepath.Join(root, "desktop", "Codex.exe")
	cli := filepath.Join(root, "npm", "Codex.exe")
	inspection := windowsInspection{verifiedExecutables: []string{desktop}}
	if !inspection.matchesExecutable(desktop) {
		t.Fatal("verified desktop executable did not match")
	}
	if inspection.matchesExecutable(cli) {
		t.Fatal("same-named CLI executable was mistaken for Codex Desktop")
	}
}
