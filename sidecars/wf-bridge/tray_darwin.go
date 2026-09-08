//go:build darwin && !wfbridge

package main

/*
#cgo LDFLAGS: -framework Cocoa
#include <stdlib.h>

void wfTrayStart(const unsigned char *iconBytes, int iconLength);
void wfTrayStop(void);
void wfTraySetDockVisible(int visible);
void wfTrayQuitMainLoop(void);
*/
import "C"

import (
	_ "embed"
	"sync"
	"unsafe"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

//go:embed build/appicon.png
var darwinTrayIcon []byte

var darwinTrayState struct {
	sync.RWMutex
	app *App
}

// startTray adds a native macOS menu-bar item without changing Wails' own
// NSApplication delegate. It remains available after the main window closes;
// the application intentionally stays in the Dock so users can reopen or quit
// it through the normal macOS controls as well.
func (a *App) startTray() {
	darwinTrayState.Lock()
	darwinTrayState.app = a
	darwinTrayState.Unlock()

	if len(darwinTrayIcon) == 0 {
		return
	}
	C.wfTrayStart((*C.uchar)(unsafe.Pointer(&darwinTrayIcon[0])), C.int(len(darwinTrayIcon)))
}

func (a *App) stopTray() {
	darwinTrayState.Lock()
	if darwinTrayState.app == a {
		darwinTrayState.app = nil
	}
	darwinTrayState.Unlock()
	C.wfTrayStop()
}

func darwinTrayApp() *App {
	darwinTrayState.RLock()
	defer darwinTrayState.RUnlock()
	return darwinTrayState.app
}

//export wfTrayShowWindow
func wfTrayShowWindow() {
	if app := darwinTrayApp(); app != nil {
		app.showMainWindow()
	}
}

//export wfTrayQuitApplication
func wfTrayQuitApplication() {
	if app := darwinTrayApp(); app != nil {
		app.requestQuit()
	}
}

func (a *App) showMainWindow() {
	if a.ctx == nil {
		return
	}
	C.wfTraySetDockVisible(1)
	runtime.Show(a.ctx)
	runtime.WindowUnminimise(a.ctx)
	runtime.WindowShow(a.ctx)
}

func (a *App) hideMainWindow() {
	if a.ctx == nil {
		return
	}
	runtime.WindowHide(a.ctx)
}

// quitNativeApplication runs on Cocoa's main queue. Calling Wails' generic
// Quit from a status-item callback can otherwise re-enter OnBeforeClose and
// turn an explicit "退出" into another hide operation.
func (a *App) quitNativeApplication() {
	C.wfTrayQuitMainLoop()
}
