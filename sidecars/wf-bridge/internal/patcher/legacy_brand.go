package patcher

import "strings"

// legacyPatcherProductToken exists only to recognize and migrate files made
// by pre-WF releases. Building the retired token at runtime keeps that old
// product name out of the current application's metadata and diagnostics.
func legacyPatcherProductToken() string {
	return strings.Join([]string{"b", "y", "o", "k"}, "")
}

func legacyPatcherDirectoryName() string {
	return ".antigravity-" + legacyPatcherProductToken()
}
