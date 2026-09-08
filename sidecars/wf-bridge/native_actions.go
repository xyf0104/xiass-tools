package main

import (
	"context"
	"errors"
	"strings"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	nativeActionOpenURL              = "open_url"
	nativeActionOpenFile             = "open_file"
	nativeActionOpenDirectory        = "open_directory"
	nativeActionSaveFile             = "save_file"
	nativeActionClaudeCodeCandidates = "claude_code_account_candidates"
	nativeActionClaudeCodeApply      = "claude_code_apply_account"
)

var errEmbeddedNativeHostUnavailable = errors.New("XIASS Tools 主应用原生操作通道不可用")

type nativeActionFilter struct {
	Name    string `json:"name"`
	Pattern string `json:"pattern"`
}

type nativeActionRequest struct {
	RequestID        string               `json:"requestId"`
	Kind             string               `json:"kind"`
	Title            string               `json:"title,omitempty"`
	DefaultDirectory string               `json:"defaultDirectory,omitempty"`
	DefaultFilename  string               `json:"defaultFilename,omitempty"`
	Filters          []nativeActionFilter `json:"filters,omitempty"`
	URL              string               `json:"url,omitempty"`
	AccountID        string               `json:"accountId,omitempty"`
	Model            string               `json:"model,omitempty"`
}

type nativeActionResult struct {
	RequestID string `json:"requestId"`
	OK        bool   `json:"ok"`
	Canceled  bool   `json:"canceled,omitempty"`
	Value     string `json:"value,omitempty"`
	Error     string `json:"error,omitempty"`
}

type nativeActionExecutor interface {
	Execute(context.Context, nativeActionRequest) (nativeActionResult, error)
	Close()
}

func runtimeFilters(filters []runtime.FileFilter) []nativeActionFilter {
	result := make([]nativeActionFilter, 0, len(filters))
	for _, filter := range filters {
		name := strings.TrimSpace(filter.DisplayName)
		pattern := strings.TrimSpace(filter.Pattern)
		if name == "" || pattern == "" {
			continue
		}
		result = append(result, nativeActionFilter{Name: name, Pattern: pattern})
	}
	return result
}

func (a *App) executeNativeAction(request nativeActionRequest) (nativeActionResult, error) {
	if a == nil || a.ctx == nil {
		return nativeActionResult{}, errors.New("助手尚未完成启动")
	}
	if !a.embeddedMode {
		return nativeActionResult{}, errors.New("原生操作代理仅用于嵌入模式")
	}
	if a.nativeActions == nil {
		return nativeActionResult{}, errEmbeddedNativeHostUnavailable
	}
	return a.nativeActions.Execute(a.ctx, request)
}

func (a *App) openFileDialog(options runtime.OpenDialogOptions) (string, error) {
	if !a.embeddedMode {
		return runtime.OpenFileDialog(a.ctx, options)
	}
	result, err := a.executeNativeAction(nativeActionRequest{
		Kind:             nativeActionOpenFile,
		Title:            options.Title,
		DefaultDirectory: options.DefaultDirectory,
		Filters:          runtimeFilters(options.Filters),
	})
	if err != nil {
		return "", err
	}
	if result.Canceled {
		return "", nil
	}
	if !result.OK || strings.TrimSpace(result.Value) == "" {
		return "", errors.New("主应用未返回有效文件")
	}
	return result.Value, nil
}

func (a *App) openDirectoryDialog(options runtime.OpenDialogOptions) (string, error) {
	if !a.embeddedMode {
		return runtime.OpenDirectoryDialog(a.ctx, options)
	}
	result, err := a.executeNativeAction(nativeActionRequest{
		Kind:             nativeActionOpenDirectory,
		Title:            options.Title,
		DefaultDirectory: options.DefaultDirectory,
	})
	if err != nil {
		return "", err
	}
	if result.Canceled {
		return "", nil
	}
	if !result.OK || strings.TrimSpace(result.Value) == "" {
		return "", errors.New("主应用未返回有效目录")
	}
	return result.Value, nil
}

func (a *App) saveFileDialog(options runtime.SaveDialogOptions) (string, error) {
	if !a.embeddedMode {
		return runtime.SaveFileDialog(a.ctx, options)
	}
	result, err := a.executeNativeAction(nativeActionRequest{
		Kind:             nativeActionSaveFile,
		Title:            options.Title,
		DefaultDirectory: options.DefaultDirectory,
		DefaultFilename:  options.DefaultFilename,
		Filters:          runtimeFilters(options.Filters),
	})
	if err != nil {
		return "", err
	}
	if result.Canceled {
		return "", nil
	}
	if !result.OK || strings.TrimSpace(result.Value) == "" {
		return "", errors.New("主应用未返回有效保存位置")
	}
	return result.Value, nil
}

func (a *App) openExternalURL(value string) error {
	if a == nil {
		return errors.New("助手尚未完成启动")
	}
	if a.ctx == nil {
		if a.embeddedMode {
			return errors.New("助手尚未完成启动")
		}
		// Standalone OAuth flows historically allow test/pre-start callers to
		// create the authorization URL without attempting a Wails runtime call.
		return nil
	}
	if !a.embeddedMode {
		runtime.BrowserOpenURL(a.ctx, value)
		return nil
	}
	result, err := a.executeNativeAction(nativeActionRequest{Kind: nativeActionOpenURL, URL: value})
	if err != nil {
		return err
	}
	if !result.OK {
		return errors.New("主应用无法打开浏览器")
	}
	return nil
}
