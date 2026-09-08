package storage

import "testing"

func TestOAuthSettingsPersistOnlyABoundedPublicClientID(t *testing.T) {
	Init(t.TempDir())
	settings := DefaultAppSettings()
	settings.OAuth.GoogleDesktopClientID = "  public-google-client.apps.googleusercontent.com  "
	if err := SaveAppSettings(settings); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadAppSettings()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := loaded.OAuth.GoogleDesktopClientID, "public-google-client.apps.googleusercontent.com"; got != want {
		t.Fatalf("saved OAuth client ID = %q, want %q", got, want)
	}

	settings.OAuth.GoogleDesktopClientID = "invalid\nclient"
	if got := NormalizeAppSettings(settings).OAuth.GoogleDesktopClientID; got != "" {
		t.Fatalf("control-bearing OAuth client ID was retained: %q", got)
	}
}
