//go:build windows

package mcpconfig

import (
	"syscall"
	"unsafe"
)

const (
	moveFileReplaceExisting = 0x1
	moveFileWriteThrough    = 0x8
)

var (
	mcpAtomicKernel32  = syscall.NewLazyDLL("kernel32.dll")
	mcpMoveFileExWProc = mcpAtomicKernel32.NewProc("MoveFileExW")
)

// MoveFileExW provides replacement semantics that os.Rename cannot promise on
// every Windows filesystem. Errors are intentionally collapsed by callers.
func replaceFileAtomic(source, destination string) error {
	sourcePointer, err := syscall.UTF16PtrFromString(source)
	if err != nil {
		return ErrOperation
	}
	destinationPointer, err := syscall.UTF16PtrFromString(destination)
	if err != nil {
		return ErrOperation
	}
	result, _, _ := mcpMoveFileExWProc.Call(
		uintptr(unsafe.Pointer(sourcePointer)),
		uintptr(unsafe.Pointer(destinationPointer)),
		moveFileReplaceExisting|moveFileWriteThrough,
	)
	if result == 0 {
		return ErrOperation
	}
	return nil
}
