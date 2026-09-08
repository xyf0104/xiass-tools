package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"antigravity-wf-assistant/internal/totp"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// TOTPStatus never contains a Base32 secret. It is safe for Wails to marshal
// into the renderer because only user-visible metadata and a generated code
// (when explicitly requested) are returned.
type TOTPStatus struct {
	OK      bool         `json:"ok"`
	Message string       `json:"message"`
	Entries []totp.Entry `json:"entries,omitempty"`
}

type TOTPCodeResult struct {
	OK      bool      `json:"ok"`
	Message string    `json:"message"`
	Code    totp.Code `json:"code"`
}

const maxTOTPImportFileBytes = 8 * 1024 * 1024

func (a *App) GetTOTPEntries() TOTPStatus {
	vault, err := a.getTOTPVault()
	if err != nil {
		return TOTPStatus{OK: false, Message: "本机验证器尚未完成初始化。"}
	}
	entries, err := vault.List()
	if err != nil {
		return TOTPStatus{OK: false, Message: "无法读取本机验证器。请检查系统凭据库权限后重试。"}
	}
	return TOTPStatus{OK: true, Message: "已读取本机验证器。", Entries: entries}
}

func (a *App) AddTOTPEntry(input totp.ImportInput) TOTPStatus {
	vault, err := a.getTOTPVault()
	if err != nil {
		return TOTPStatus{OK: false, Message: "本机验证器尚未完成初始化。"}
	}
	if _, err := vault.Add(input); err != nil {
		return TOTPStatus{OK: false, Message: "无法保存验证器。请检查导入格式与系统凭据库权限后重试。"}
	}
	return a.GetTOTPEntries()
}

func (a *App) GenerateTOTPCode(id string) TOTPCodeResult {
	vault, err := a.getTOTPVault()
	if err != nil {
		return TOTPCodeResult{OK: false, Message: "本机验证器尚未完成初始化。"}
	}
	code, err := vault.Generate(strings.TrimSpace(id), time.Now())
	if err != nil {
		return TOTPCodeResult{OK: false, Message: "无法生成动态验证码。请确认该验证器仍存在且系统凭据库可用。"}
	}
	return TOTPCodeResult{OK: true, Message: "动态验证码已生成。", Code: code}
}

func (a *App) DeleteTOTPEntry(id string) TOTPStatus {
	vault, err := a.getTOTPVault()
	if err != nil {
		return TOTPStatus{OK: false, Message: "本机验证器尚未完成初始化。"}
	}
	if err := vault.Delete(strings.TrimSpace(id)); err != nil {
		return TOTPStatus{OK: false, Message: "无法删除验证器。请检查系统凭据库权限后重试。"}
	}
	return a.GetTOTPEntries()
}

// ExportTOTPEncrypted prompts for a destination only after a user supplies a
// separate export password. It never exports to diagnostics, localStorage, or
// an automatic cloud destination.
func (a *App) ExportTOTPEncrypted(password string) Result {
	if a.ctx == nil {
		return Result{OK: false, Message: "助手尚未完成启动，请稍后再试。"}
	}
	vault, err := a.getTOTPVault()
	if err != nil {
		return Result{OK: false, Message: "本机验证器尚未完成初始化。"}
	}
	data, err := vault.ExportEncrypted(password)
	if err != nil {
		return Result{OK: false, Message: "无法创建加密验证器备份。请确认导出密码与系统凭据库后重试。"}
	}
	destination, err := a.saveFileDialog(runtime.SaveDialogOptions{
		Title:           "导出加密验证器备份",
		DefaultFilename: "XIASS-Tools-TOTP-" + time.Now().Format("20060102-150405") + ".json",
		Filters: []runtime.FileFilter{{
			DisplayName: "XIASS Tools 加密备份 (*.json)", Pattern: "*.json",
		}},
	})
	if err != nil {
		return Result{OK: false, Message: "无法打开备份保存窗口。"}
	}
	if strings.TrimSpace(destination) == "" {
		return Result{OK: true, Message: "已取消导出加密验证器备份。"}
	}
	if !strings.EqualFold(filepath.Ext(destination), ".json") {
		destination += ".json"
	}
	if err := writeNewSensitiveExport(destination, data); err != nil {
		return Result{OK: false, Message: "无法保存加密验证器备份。请确认目标位置可写且文件不存在。"}
	}
	return Result{OK: true, Message: "已导出加密验证器备份。请妥善保管导出密码与文件。"}
}

// ImportTOTPEncrypted keeps the selected backup path and its contents inside
// the native process. The WebView receives only a redacted result and public
// entry metadata after the system credential vault commit has succeeded.
func (a *App) ImportTOTPEncrypted(password string) TOTPStatus {
	if a.ctx == nil {
		return TOTPStatus{OK: false, Message: "助手尚未完成启动，请稍后再试。"}
	}
	source, err := a.openFileDialog(runtime.OpenDialogOptions{
		Title: "导入加密验证器备份",
		Filters: []runtime.FileFilter{{
			DisplayName: "XIASS Tools 加密备份 (*.json)", Pattern: "*.json",
		}},
	})
	if err != nil {
		return TOTPStatus{OK: false, Message: "无法打开加密备份选择窗口。"}
	}
	if strings.TrimSpace(source) == "" {
		status := a.GetTOTPEntries()
		if status.OK {
			status.Message = "已取消导入加密验证器备份。"
		}
		return status
	}
	data, err := readSensitiveTOTPImport(source, maxTOTPImportFileBytes)
	if err != nil {
		return TOTPStatus{OK: false, Message: "无法安全读取加密验证器备份。请确认文件完整、为普通文件且大小受支持。"}
	}
	defer wipeTOTPImportData(data)
	return a.importTOTPEncryptedData(data, password)
}

func (a *App) importTOTPEncryptedData(data []byte, password string) TOTPStatus {
	vault, err := a.getTOTPVault()
	if err != nil {
		return TOTPStatus{OK: false, Message: "本机验证器尚未完成初始化。"}
	}
	imported, err := vault.ImportEncrypted(data, password)
	if err != nil {
		return TOTPStatus{OK: false, Message: "无法导入加密验证器备份。请确认导出密码、文件完整性，以及系统凭据库权限后重试。"}
	}
	entries, err := vault.List()
	if err != nil {
		// Import completed transactionally. Do not rewrite that fact as a failed
		// operation just because the follow-up metadata refresh is unavailable.
		return TOTPStatus{OK: true, Message: fmt.Sprintf("已导入 %d 个验证器。请刷新列表以确认当前状态。", len(imported))}
	}
	return TOTPStatus{OK: true, Message: fmt.Sprintf("已导入 %d 个验证器。", len(imported)), Entries: entries}
}

func (a *App) getTOTPVault() (*totp.Vault, error) {
	if a == nil || a.totpVault == nil {
		return nil, errors.New("系统凭据库尚未完成初始化")
	}
	return a.totpVault, nil
}

// writeNewSensitiveExport refuses to overwrite a file selected by mistake.
// A user can explicitly choose a different name; this avoids destructive
// replacement of an existing encrypted backup.
func writeNewSensitiveExport(destination string, data []byte) error {
	destination = filepath.Clean(strings.TrimSpace(destination))
	if destination == "" || destination == "." {
		return errors.New("导出路径无效")
	}
	if _, err := os.Lstat(destination); err == nil {
		return errors.New("目标文件已存在，请选择新的文件名以避免覆盖现有备份")
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	success := false
	defer func() {
		_ = file.Close()
		if !success {
			_ = os.Remove(destination)
		}
	}()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	success = true
	return nil
}

func readSensitiveTOTPImport(source string, limit int64) ([]byte, error) {
	if limit <= 0 {
		return nil, errors.New("验证器备份大小限制无效")
	}
	source = filepath.Clean(strings.TrimSpace(source))
	if source == "" || source == "." {
		return nil, errors.New("验证器备份路径无效")
	}
	initial, err := os.Lstat(source)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, errors.New("验证器备份不存在")
	}
	if err != nil || initial.Mode()&os.ModeSymlink != 0 || !initial.Mode().IsRegular() || initial.Size() < 0 || initial.Size() > limit {
		return nil, errors.New("验证器备份不是受支持的普通文件")
	}
	file, err := os.Open(source)
	if err != nil {
		return nil, errors.New("无法打开验证器备份")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(initial, opened) {
		return nil, errors.New("验证器备份在读取前发生变化")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(data)) > limit {
		wipeTOTPImportData(data)
		return nil, errors.New("验证器备份无法安全读取")
	}
	final, err := os.Lstat(source)
	if err != nil || final.Mode()&os.ModeSymlink != 0 || !final.Mode().IsRegular() || !os.SameFile(initial, final) || final.Size() != initial.Size() {
		wipeTOTPImportData(data)
		return nil, errors.New("验证器备份在读取过程中发生变化")
	}
	return data, nil
}

func wipeTOTPImportData(data []byte) {
	for index := range data {
		data[index] = 0
	}
}
