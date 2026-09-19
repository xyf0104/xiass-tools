//go:build windows

package codexdesktop

import (
	"context"
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

// systemProcessLister uses the Toolhelp process snapshot and
// QueryFullProcessImageNameW. It reads neither command lines nor environment
// blocks, so API keys and user prompts cannot appear in this observation.
type systemProcessLister struct{}

func (systemProcessLister) List(ctx context.Context) ([]Process, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snapshot)

	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	if err := windows.Process32First(snapshot, &entry); err != nil {
		return nil, err
	}
	processes := make([]Process, 0, 8)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := windows.UTF16ToString(entry.ExeFile[:])
		if isSupportedWindowsExecutable(name) {
			executable, imageErr := windowsProcessImagePath(entry.ProcessID)
			if imageErr != nil {
				// The process may have exited after the snapshot. ERROR_INVALID_PARAMETER
				// is the documented result for a no-longer-existing PID; every other
				// failure is unsafe because it may hide a running Codex Desktop.
				if !errors.Is(imageErr, windows.ERROR_INVALID_PARAMETER) {
					return nil, imageErr
				}
			} else if executable != "" {
				processes = append(processes, Process{Executable: executable, PID: entry.ProcessID})
			}
		}
		err = windows.Process32Next(snapshot, &entry)
		if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	return processes, nil
}

func windowsProcessImagePath(pid uint32) (string, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(handle)
	buffer := make([]uint16, 32768)
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(handle, 0, &buffer[0], &size); err != nil {
		return "", err
	}
	return safeWindowsPath(windows.UTF16ToString(buffer[:size])), nil
}
