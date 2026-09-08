//go:build darwin

package patcher

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func darwinSyntheticASAR(t *testing.T, path, headerJSON string) string {
	t.Helper()
	headerSize := align4(8 + len(headerJSON))
	data := make([]byte, 8+headerSize+4)
	binary.LittleEndian.PutUint32(data[0:4], 4)
	binary.LittleEndian.PutUint32(data[4:8], uint32(headerSize))
	binary.LittleEndian.PutUint32(data[8:12], uint32(headerSize-4))
	binary.LittleEndian.PutUint32(data[12:16], uint32(len(headerJSON)))
	copy(data[16:], headerJSON)
	copy(data[8+headerSize:], "BODY")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(headerJSON))
	return hex.EncodeToString(digest[:])
}

func darwinIntegrityPlist(hash string) []byte {
	return []byte(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>com.google.antigravity</string>
<key>ElectronAsarIntegrity</key><dict>
  <key>Resources/app.asar</key><dict>
    <key>algorithm</key><string>SHA256</string>
    <key>hash</key><string>` + hash + `</string>
  </dict>
</dict>
<key>KeepMe</key><string>unchanged</string>
</dict></plist>`)
}

func writeDarwinAgentIntegrityFixture(t *testing.T, appPath, asarPath string) []byte {
	t.Helper()
	hash, err := darwinASARHeaderHash(asarPath)
	if err != nil {
		t.Fatal(err)
	}
	data := darwinIntegrityPlist(hash)
	path := filepath.Join(appPath, "Contents", "Info.plist")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return data
}

func newDarwinAgentIntegrityFixture(t *testing.T) (darwinTargets, string, string, string) {
	t.Helper()
	app := filepath.Join(t.TempDir(), "Antigravity 2.app")
	active := filepath.Join(app, "Contents", "Resources", "app.asar")
	candidate := filepath.Join(t.TempDir(), "candidate.app.asar")
	activeHash := darwinSyntheticASAR(t, active, `{"files":{"dist":{"files":{"main.js":{"size":1,"offset":"0"}}}}}`)
	candidateHash := darwinSyntheticASAR(t, candidate, `{"files":{"dist":{"files":{"main.js":{"size":2,"offset":"0"}}}}}`)
	plist := filepath.Join(app, "Contents", "Info.plist")
	if err := os.WriteFile(plist, darwinIntegrityPlist(activeHash), 0o644); err != nil {
		t.Fatal(err)
	}
	return darwinTargets{app: app, asar: active, kind: "agent"}, candidate, activeHash, candidateHash
}

func TestDarwinASARHeaderHashUsesExactJSONBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.asar")
	header := `{"files":{},"padding-sensitive":"yes"}`
	expected := darwinSyntheticASAR(t, path, header)
	actual, err := darwinASARHeaderHash(path)
	if err != nil {
		t.Fatal(err)
	}
	if actual != expected {
		t.Fatalf("header hash=%s want=%s", actual, expected)
	}
}

func TestDarwinASARHeaderHashRejectsUnknownFraming(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.asar")
	darwinSyntheticASAR(t, path, `{"files":{}}`)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	binary.LittleEndian.PutUint32(data[0:4], 8)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if hash, err := darwinASARHeaderHash(path); err == nil || hash != "" {
		t.Fatalf("unknown ASAR framing must fail closed: hash=%q err=%v", hash, err)
	}
}

func TestDarwinAgentASARIntegrityPatchAndVerify(t *testing.T) {
	target, candidate, activeHash, candidateHash := newDarwinAgentIntegrityFixture(t)
	plan, err := prepareDarwinAgentASARIntegrityPatch(target, candidate)
	if err != nil || plan == nil || !plan.changed {
		t.Fatalf("plan=%#v err=%v", plan, err)
	}
	if !strings.Contains(string(plan.original), activeHash) || !strings.Contains(string(plan.updated), candidateHash) || !strings.Contains(string(plan.updated), "KeepMe") {
		t.Fatalf("Info.plist plan was not minimal: %s", plan.updated)
	}
	if err := writePatchPlans([]*patchPlan{plan}); err != nil {
		t.Fatal(err)
	}
	candidateData, err := os.ReadFile(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target.asar, candidateData, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyDarwinAgentASARIntegrity(target); err != nil {
		t.Fatal(err)
	}
}

func TestDarwinAgentASARIntegrityPatchTakesOverThirdPartyCurrentHash(t *testing.T) {
	target, candidate, _, candidateHash := newDarwinAgentIntegrityFixture(t)
	plist := filepath.Join(target.app, "Contents", "Info.plist")
	other := strings.Repeat("0", sha256.Size*2)
	if err := os.WriteFile(plist, darwinIntegrityPlist(other), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := prepareDarwinAgentASARIntegrityPatch(target, candidate)
	if err != nil || plan == nil || !plan.changed {
		t.Fatalf("third-party integrity hash was not accepted: plan=%#v err=%v", plan, err)
	}
	if !strings.Contains(string(plan.original), other) || !strings.Contains(string(plan.updated), candidateHash) {
		t.Fatalf("third-party integrity plan did not replace only the tracked hash: %s", plan.updated)
	}
}

func TestDarwinAgentASARIntegrityRejectsBinaryUnknownAndDuplicateMetadata(t *testing.T) {
	target, candidate, activeHash, _ := newDarwinAgentIntegrityFixture(t)
	plist := filepath.Join(target.app, "Contents", "Info.plist")
	tests := []struct {
		name string
		data []byte
	}{
		{name: "binary plist", data: []byte("bplist00not-supported")},
		{name: "unknown algorithm", data: []byte(strings.ReplaceAll(string(darwinIntegrityPlist(activeHash)), "SHA256", "SHA512"))},
		{name: "duplicate hash key", data: []byte(strings.Replace(string(darwinIntegrityPlist(activeHash)), "<key>hash</key>", "<key>hash</key><string>"+activeHash+"</string><key>hash</key>", 1))},
		{name: "duplicate hash bytes", data: []byte(strings.Replace(string(darwinIntegrityPlist(activeHash)), "<key>KeepMe</key><string>unchanged</string>", "<key>KeepMe</key><string>"+activeHash+"</string>", 1))},
		{name: "duplicate archive entry", data: []byte(strings.Replace(string(darwinIntegrityPlist(activeHash)), "</dict>\n</dict>\n<key>KeepMe", "</dict><key>Resources/app.asar</key><dict><key>algorithm</key><string>SHA256</string><key>hash</key><string>"+activeHash+"</string></dict>\n</dict>\n<key>KeepMe", 1))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(plist, test.data, 0o644); err != nil {
				t.Fatal(err)
			}
			if plan, err := prepareDarwinAgentASARIntegrityPatch(target, candidate); err == nil || plan != nil {
				t.Fatalf("unknown metadata must fail closed: plan=%#v err=%v", plan, err)
			}
		})
	}
}

func TestDarwinAgentASARIntegrityOfficialReadOnly(t *testing.T) {
	app := strings.TrimSpace(os.Getenv("ANTIGRAVITY_AGENT_APP"))
	if app == "" {
		t.Skip("set ANTIGRAVITY_AGENT_APP to a vendor Antigravity 2.app for read-only verification")
	}
	target := darwinTargets{
		app:  app,
		asar: filepath.Join(app, "Contents", "Resources", "app.asar"),
		kind: "agent",
	}
	if err := verifyDarwinAgentASARIntegrity(target); err != nil {
		t.Fatalf("vendor ElectronAsarIntegrity verification failed: %v", err)
	}
}
