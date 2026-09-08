package patcher

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestASARRoundTripRecomputesIntegrity(t *testing.T) {
	root := &asarNode{Files: map[string]*asarNode{}}
	fixture := &asarArchive{root: root}
	original := filepath.Join(t.TempDir(), "fixture.asar")
	if err := fixture.write(original, map[string][]byte{
		"dist/main.js":           []byte("\"use strict\";"),
		"dist/languageServer.js": []byte("original endpoint"),
	}); err != nil {
		t.Fatal(err)
	}
	archive, err := readASAR(original)
	if err != nil {
		t.Fatal(err)
	}
	candidate := filepath.Join(t.TempDir(), "candidate.asar")
	updated := []byte("patched endpoint")
	if err := archive.write(candidate, map[string][]byte{"dist/languageServer.js": updated}); err != nil {
		t.Fatal(err)
	}
	patched, err := readASAR(candidate)
	if err != nil {
		t.Fatal(err)
	}
	data, err := patched.readFile("dist/languageServer.js")
	if err != nil || !bytes.Equal(data, updated) {
		t.Fatalf("round trip mismatch: %q, %v", data, err)
	}
	node, _ := patched.node("dist/languageServer.js")
	if node.Integrity == nil || node.Integrity.Hash != asarFileIntegrity(updated).Hash {
		t.Fatalf("integrity was not updated: %#v", node.Integrity)
	}
	if info, err := os.Stat(candidate); err != nil || info.Size() == 0 {
		t.Fatalf("candidate missing or empty: %v", err)
	}
}
