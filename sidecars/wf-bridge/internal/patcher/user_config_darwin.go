//go:build darwin

package patcher

// This file deliberately uses the same official user-setting connection path
// verified by the v1.5.1 Windows implementation.  It is intentionally kept
// Darwin-specific because locating a product's per-user settings directory is
// platform-dependent; the JSONC editor itself is conservative and operates on
// bytes so comments, formatting, and unrelated settings are preserved.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const darwinCloudCodeSetting = "jetski.cloudCodeUrl"

type darwinProductMetadata struct {
	NameShort       string `json:"nameShort"`
	DataFolderName  string `json:"dataFolderName"`
	ApplicationName string `json:"applicationName"`
}

type darwinJSONCMember struct {
	key       string
	keyBeg    int
	valueBeg  int
	valueEnd  int
	memberEnd int
	hasComma  bool
}

type darwinJSONCObject struct {
	close   int
	members []darwinJSONCMember
}

// darwinUserConfigDirectory is a narrow test seam. Production uses the
// platform-supported user configuration directory (normally
// ~/Library/Application Support on macOS) and never reads an arbitrary
// application directory from the environment.
var darwinUserConfigDirectory = os.UserConfigDir

// darwinTargetUserSettingsPath accepts a target only after proving that both
// its main process and bundled extension expose the official user-level
// configuration chain. A filename, an old helper marker, or an arbitrary URL
// is never enough to authorise a write to a user's settings.json.
func darwinTargetUserSettingsPath(target darwinTargets) (string, string, error) {
	if target.kind != "ide" || target.app == "" || target.main == "" || target.extensionEntry == "" {
		return "", "", fmt.Errorf("该安装尚未验证官方用户级代理设置接入；为保护完整性，助手不会修改安装目录")
	}
	mainSource, err := os.ReadFile(target.main)
	if err != nil {
		return "", "", fmt.Errorf("读取主进程结构失败: %w", err)
	}
	extensionSource, err := os.ReadFile(target.extensionEntry)
	if err != nil {
		return "", "", fmt.Errorf("读取扩展结构失败: %w", err)
	}
	if !strings.Contains(string(mainSource), darwinCloudCodeSetting) ||
		!strings.Contains(string(extensionSource), darwinCloudCodeSetting) ||
		!strings.Contains(string(extensionSource), "--cloud_code_endpoint") {
		return "", "", fmt.Errorf("未找到官方 %s 配置链路；请先用官方安装器覆盖重装后再连接", darwinCloudCodeSetting)
	}

	productPath := filepath.Join(target.app, "Contents", "Resources", "app", "product.json")
	productData, err := os.ReadFile(productPath)
	if err != nil {
		return "", "", fmt.Errorf("读取官方产品信息失败: %w", err)
	}
	var product darwinProductMetadata
	if err := json.Unmarshal(productData, &product); err != nil {
		return "", "", fmt.Errorf("解析官方产品信息失败: %w", err)
	}
	configDir, err := darwinUserConfigDirectory()
	if err != nil {
		return "", "", fmt.Errorf("定位 macOS 用户配置目录失败: %w", err)
	}
	names := darwinUniqueNonEmptyStrings(product.NameShort, product.DataFolderName, product.ApplicationName)
	if len(names) == 0 {
		return "", "", fmt.Errorf("官方产品信息未提供用户配置目录名称")
	}
	candidates := darwinProductConfigDirectories(configDir, names)
	for _, directory := range candidates {
		candidate := filepath.Join(directory, "User", "settings.json")
		if existingFile(candidate) != "" {
			return candidate, "user-settings", nil
		}
	}
	for _, directory := range candidates {
		if info, statErr := os.Stat(directory); statErr == nil && info.IsDir() {
			return filepath.Join(directory, "User", "settings.json"), "user-settings", nil
		}
	}
	return filepath.Join(candidates[0], "User", "settings.json"), "user-settings", nil
}

func darwinUniqueNonEmptyStrings(values ...string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if !darwinSafeProductConfigName(value) || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}

func darwinSafeProductConfigName(value string) bool {
	return value != "" && value != "." && value != ".." && filepath.Base(value) == value &&
		!strings.ContainsAny(value, `/\\`)
}

// darwinProductConfigDirectories keeps the vendor-declared spelling first,
// then adds the actual on-disk spelling of case-insensitive matches. This is
// important on case-sensitive APFS volumes where older Antigravity builds
// used both "Antigravity" and "antigravity" as their support directory.
func darwinProductConfigDirectories(configDir string, names []string) []string {
	result := make([]string, 0, len(names)*2)
	seen := make(map[string]bool, len(names)*2)
	add := func(path string) {
		path = filepath.Clean(path)
		if !seen[path] {
			seen[path] = true
			result = append(result, path)
		}
	}
	entries, err := os.ReadDir(configDir)
	if err == nil {
		for _, name := range names {
			for _, entry := range entries {
				if entry.IsDir() && entry.Name() == name {
					add(filepath.Join(configDir, entry.Name()))
				}
			}
			for _, entry := range entries {
				if entry.IsDir() && strings.EqualFold(entry.Name(), name) {
					add(filepath.Join(configDir, entry.Name()))
				}
			}
		}
	}
	for _, name := range names {
		add(filepath.Join(configDir, name))
	}
	return result
}

func darwinCloudCodeSettingIsConfigured(path, endpoint string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	document, err := parseDarwinJSONCObject(string(data))
	if err != nil {
		return false
	}
	for _, member := range document.members {
		if member.key != darwinCloudCodeSetting {
			continue
		}
		value, err := strconv.Unquote(string(data[member.valueBeg:member.valueEnd]))
		return err == nil && value == endpoint
	}
	return false
}

// prepareDarwinEnsureCloudCodeSetting plans a minimal settings.json mutation.
// Existing string endpoints are replaced in place after the caller records an
// exact pre-upgrade backup. Comments, formatting and unrelated settings remain
// byte-identical.
func prepareDarwinEnsureCloudCodeSetting(path, endpoint string) (*patchPlan, bool, error) {
	data, err := os.ReadFile(path)
	mode := os.FileMode(0o600)
	if os.IsNotExist(err) {
		data = []byte("{\n}\n")
	} else if err != nil {
		return nil, false, err
	} else if info, statErr := os.Stat(path); statErr != nil {
		return nil, false, statErr
	} else {
		mode = info.Mode()
	}
	document, err := parseDarwinJSONCObject(string(data))
	if err != nil {
		return nil, false, fmt.Errorf("%s 不是可安全编辑的 JSONC 设置文件: %w", path, err)
	}
	for _, member := range document.members {
		if member.key != darwinCloudCodeSetting {
			continue
		}
		value, unquoteErr := strconv.Unquote(string(data[member.valueBeg:member.valueEnd]))
		if unquoteErr != nil {
			return nil, false, fmt.Errorf("%s 的 %s 不是字符串，已拒绝覆盖", path, darwinCloudCodeSetting)
		}
		if value == endpoint {
			return nil, false, nil
		}
		updated := append([]byte(nil), data[:member.valueBeg]...)
		updated = append(updated, []byte(strconv.Quote(endpoint))...)
		updated = append(updated, data[member.valueEnd:]...)
		return &patchPlan{path: path, original: data, updated: updated, mode: mode, changed: true}, true, nil
	}
	entry := `  "` + darwinCloudCodeSetting + `": ` + strconv.Quote(endpoint)
	updated := string(data)
	if len(document.members) == 0 {
		updated = updated[:document.close] + "\n" + entry + "\n" + updated[document.close:]
	} else {
		last := document.members[len(document.members)-1]
		close := document.close
		if !last.hasComma {
			updated = updated[:last.valueEnd] + "," + updated[last.valueEnd:]
			close++
		}
		updated = updated[:close] + "\n" + entry + "\n" + updated[close:]
	}
	return &patchPlan{path: path, original: data, updated: []byte(updated), mode: mode, changed: true}, true, nil
}

func prepareDarwinRemoveCloudCodeSetting(path, endpoint string) (*patchPlan, bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	document, err := parseDarwinJSONCObject(string(data))
	if err != nil {
		return nil, false, fmt.Errorf("%s 不是可安全编辑的 JSONC 设置文件: %w", path, err)
	}
	for _, member := range document.members {
		if member.key != darwinCloudCodeSetting {
			continue
		}
		value, unquoteErr := strconv.Unquote(string(data[member.valueBeg:member.valueEnd]))
		if unquoteErr != nil || value != endpoint {
			return nil, false, nil
		}
		updated := append([]byte(nil), data[:member.keyBeg]...)
		updated = append(updated, data[member.memberEnd:]...)
		info, statErr := os.Stat(path)
		if statErr != nil {
			return nil, false, statErr
		}
		return &patchPlan{path: path, original: data, updated: updated, mode: info.Mode(), changed: true}, true, nil
	}
	return nil, false, nil
}

// parseDarwinJSONCObject parses exactly one top-level object. It does not try
// to normalise the document, which is why comments and unrelated formatting
// survive an add/remove round trip unchanged.
func parseDarwinJSONCObject(source string) (darwinJSONCObject, error) {
	position, err := skipDarwinJSONCTrivia(source, 0)
	if err != nil {
		return darwinJSONCObject{}, err
	}
	if position >= len(source) || source[position] != '{' {
		return darwinJSONCObject{}, fmt.Errorf("根节点不是对象")
	}
	position++
	document := darwinJSONCObject{}
	for {
		position, err = skipDarwinJSONCTrivia(source, position)
		if err != nil {
			return darwinJSONCObject{}, err
		}
		if position >= len(source) {
			return darwinJSONCObject{}, fmt.Errorf("对象未闭合")
		}
		if source[position] == '}' {
			document.close = position
			return document, nil
		}
		if source[position] != '"' {
			return darwinJSONCObject{}, fmt.Errorf("属性名不是字符串")
		}
		keyBeg := position
		keyEnd, key, err := scanDarwinJSONCString(source, position)
		if err != nil {
			return darwinJSONCObject{}, err
		}
		position, err = skipDarwinJSONCTrivia(source, keyEnd)
		if err != nil || position >= len(source) || source[position] != ':' {
			return darwinJSONCObject{}, fmt.Errorf("属性 %q 缺少冒号", key)
		}
		valueBeg, err := skipDarwinJSONCTrivia(source, position+1)
		if err != nil {
			return darwinJSONCObject{}, err
		}
		valueEnd, err := scanDarwinJSONCValue(source, valueBeg)
		if err != nil {
			return darwinJSONCObject{}, err
		}
		position, err = skipDarwinJSONCTrivia(source, valueEnd)
		if err != nil || position >= len(source) {
			return darwinJSONCObject{}, fmt.Errorf("属性 %q 的值未结束", key)
		}
		member := darwinJSONCMember{key: key, keyBeg: keyBeg, valueBeg: valueBeg, valueEnd: valueEnd, memberEnd: valueEnd}
		if source[position] == ',' {
			member.hasComma = true
			position++
			member.memberEnd = position
		} else if source[position] != '}' {
			return darwinJSONCObject{}, fmt.Errorf("属性 %q 后缺少逗号", key)
		}
		document.members = append(document.members, member)
	}
}

func skipDarwinJSONCTrivia(source string, position int) (int, error) {
	for position < len(source) {
		if strings.ContainsRune(" \t\r\n\ufeff", rune(source[position])) {
			position++
			continue
		}
		if source[position] != '/' || position+1 >= len(source) {
			return position, nil
		}
		switch source[position+1] {
		case '/':
			position += 2
			for position < len(source) && source[position] != '\n' {
				position++
			}
		case '*':
			end := strings.Index(source[position+2:], "*/")
			if end < 0 {
				return 0, fmt.Errorf("块注释未闭合")
			}
			position += end + 4
		default:
			return position, nil
		}
	}
	return position, nil
}

func scanDarwinJSONCString(source string, position int) (int, string, error) {
	start := position
	position++
	for position < len(source) {
		switch source[position] {
		case '\\':
			position += 2
		case '"':
			position++
			value, err := strconv.Unquote(source[start:position])
			return position, value, err
		default:
			position++
		}
	}
	return 0, "", fmt.Errorf("字符串未闭合")
}

func scanDarwinJSONCValue(source string, position int) (int, error) {
	if position >= len(source) {
		return 0, fmt.Errorf("缺少值")
	}
	if source[position] == '"' {
		end, _, err := scanDarwinJSONCString(source, position)
		return end, err
	}
	if source[position] != '{' && source[position] != '[' {
		end := position
		for end < len(source) && !strings.ContainsRune(" \t\r\n,}]", rune(source[end])) {
			end++
		}
		if end == position {
			return 0, fmt.Errorf("无效值")
		}
		return end, nil
	}
	stack := []byte{source[position]}
	for position++; position < len(source); position++ {
		if source[position] == '"' {
			end, _, err := scanDarwinJSONCString(source, position)
			if err != nil {
				return 0, err
			}
			position = end - 1
			continue
		}
		if source[position] == '/' {
			next, err := skipDarwinJSONCTrivia(source, position)
			if err != nil {
				return 0, err
			}
			if next != position {
				position = next - 1
				continue
			}
		}
		switch source[position] {
		case '{', '[':
			stack = append(stack, source[position])
		case '}', ']':
			if len(stack) == 0 || (source[position] == '}' && stack[len(stack)-1] != '{') || (source[position] == ']' && stack[len(stack)-1] != '[') {
				return 0, fmt.Errorf("数组或对象闭合不匹配")
			}
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				return position + 1, nil
			}
		}
	}
	return 0, fmt.Errorf("数组或对象未闭合")
}
