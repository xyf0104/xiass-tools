//go:build darwin

package launcher

import (
	"reflect"
	"testing"
)

func TestDarwinRunningPIDsMatchesOnlySelectedBundle(t *testing.T) {
	output := `
  101 /Applications/Antigravity.app/Contents/MacOS/Electron
  102 /Applications/Antigravity.app/Contents/Frameworks/Antigravity Helper.app/Contents/MacOS/Antigravity Helper --type=renderer
  201 /Applications/Antigravity 2.0.app/Contents/MacOS/Antigravity
  301 /Applications/Another.app/Contents/MacOS/Another --name=Antigravity
`
	got := darwinRunningPIDs(output, "/Applications/Antigravity.app")
	if want := []int{101, 102}; !reflect.DeepEqual(got, want) {
		t.Fatalf("running PIDs = %v, want %v", got, want)
	}
}

func TestEscapeAppleScriptString(t *testing.T) {
	got := escapeAppleScriptString(`/Applications/WF "Preview".app`)
	if want := `/Applications/WF \"Preview\".app`; got != want {
		t.Fatalf("escaped path = %q, want %q", got, want)
	}
}
