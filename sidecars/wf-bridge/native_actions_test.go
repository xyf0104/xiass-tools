package main

import (
	"context"
	"errors"
	"testing"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

func TestEmbeddedNativeActionsFailClosedWithoutHostExecutor(t *testing.T) {
	app := &App{embeddedMode: true, ctx: context.Background()}

	if _, err := app.saveFileDialog(runtime.SaveDialogOptions{}); !errors.Is(err, errEmbeddedNativeHostUnavailable) {
		t.Fatalf("embedded save dialog should require the Tauri host, got %v", err)
	}
	if err := app.openExternalURL("https://example.com"); !errors.Is(err, errEmbeddedNativeHostUnavailable) {
		t.Fatalf("embedded browser action should require the Tauri host, got %v", err)
	}
}
