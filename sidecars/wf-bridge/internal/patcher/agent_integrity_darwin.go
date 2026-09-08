//go:build darwin

package patcher

// Electron verifies a packaged application's ASAR before loading JavaScript.
// On macOS the expected digest is stored in Contents/Info.plist under:
//
//   ElectronAsarIntegrity -> Resources/app.asar -> { algorithm, hash }
//
// The digest is SHA-256 over the exact JSON bytes in the ASAR header. It is
// deliberately not a digest of the complete archive or of the padded header.
// Keep this code independent from macOS code signing: synchronising Electron's
// integrity metadata does not make a modified vendor bundle Developer-ID
// signed again.

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const darwinMaxASARHeaderSize = 64 * 1024 * 1024

type darwinPlistEntry struct {
	key   string
	value *darwinPlistValue
}

type darwinPlistValue struct {
	kind    string
	text    string
	entries []darwinPlistEntry
	values  []*darwinPlistValue
}

// darwinASARHeaderHash reads and validates Chromium Pickle framing used by
// classic Electron ASAR archives, then hashes only the embedded JSON string.
// Strict length and JSON checks prevent a corrupt or unknown archive from
// producing a plausible-looking integrity value.
func darwinASARHeaderHash(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	var prefix [16]byte
	if _, err := io.ReadFull(file, prefix[:]); err != nil {
		return "", fmt.Errorf("读取 ASAR 完整性头失败: %w", err)
	}
	firstPayloadSize := uint64(binary.LittleEndian.Uint32(prefix[0:4]))
	headerSize := uint64(binary.LittleEndian.Uint32(prefix[4:8]))
	headerPayloadSize := uint64(binary.LittleEndian.Uint32(prefix[8:12]))
	jsonSize := uint64(binary.LittleEndian.Uint32(prefix[12:16]))

	// The first Pickle contains one uint32 (the second Pickle's total size).
	if firstPayloadSize != 4 {
		return "", fmt.Errorf("ASAR 外层头结构尚未验证: payload=%d", firstPayloadSize)
	}
	if headerSize < 8 || headerSize > darwinMaxASARHeaderSize {
		return "", fmt.Errorf("ASAR 头长度异常: %d", headerSize)
	}
	if headerPayloadSize+4 != headerSize {
		return "", fmt.Errorf("ASAR 内层头长度不匹配: %d != %d", headerPayloadSize+4, headerSize)
	}
	// Inner Pickle layout is: payload-size, string-size, JSON, zero padding.
	expectedHeaderSize := uint64(align4(8 + int(jsonSize)))
	if jsonSize == 0 || expectedHeaderSize != headerSize {
		return "", fmt.Errorf("ASAR JSON 头长度不匹配: json=%d header=%d", jsonSize, headerSize)
	}
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if uint64(info.Size()) < 8+headerSize {
		return "", fmt.Errorf("ASAR 文件短于声明的头长度: %d < %d", info.Size(), 8+headerSize)
	}

	headerTail := make([]byte, headerSize-8)
	if _, err := io.ReadFull(file, headerTail); err != nil {
		return "", fmt.Errorf("读取 ASAR JSON 头失败: %w", err)
	}
	if jsonSize > uint64(len(headerTail)) {
		return "", errors.New("ASAR JSON 头越界")
	}
	jsonBytes := headerTail[:jsonSize]
	padding := headerTail[jsonSize:]
	if len(padding) > 3 || !bytes.Equal(padding, make([]byte, len(padding))) {
		return "", errors.New("ASAR JSON 头填充结构尚未验证")
	}
	var root struct {
		Files json.RawMessage `json:"files"`
	}
	if err := json.Unmarshal(jsonBytes, &root); err != nil || len(root.Files) == 0 || bytes.Equal(root.Files, []byte("null")) {
		if err != nil {
			return "", fmt.Errorf("解析 ASAR JSON 头失败: %w", err)
		}
		return "", errors.New("ASAR JSON 头缺少文件表")
	}
	digest := sha256.Sum256(jsonBytes)
	return hex.EncodeToString(digest[:]), nil
}

func darwinAgentInfoPlistPath(target darwinTargets) (string, error) {
	if target.kind != "agent" || strings.TrimSpace(target.app) == "" || strings.TrimSpace(target.asar) == "" {
		return "", errors.New("不是可验证的 Antigravity 2.0 目标")
	}
	expectedASAR := filepath.Clean(filepath.Join(target.app, "Contents", "Resources", "app.asar"))
	if filepath.Clean(target.asar) != expectedASAR {
		return "", errors.New("Antigravity 2.0 app.asar 路径结构尚未验证")
	}
	return filepath.Join(target.app, "Contents", "Info.plist"), nil
}

// prepareDarwinAgentASARIntegrityPatch creates the companion Info.plist plan
// for a prepared app.asar candidate. A parsed current hash may belong to the
// vendor, an older WF release, or a third-party archive; the entire plist and
// active archive are backed up before the transaction replaces that one value.
func prepareDarwinAgentASARIntegrityPatch(target darwinTargets, candidateASAR string) (*patchPlan, error) {
	return prepareDarwinAgentASARIntegrityPatchFrom(target, candidateASAR, target.asar)
}

// prepareDarwinAgentASARIntegrityPatchFrom retains its third argument for
// compatibility with historical regression fixtures. Upgrade planning is now
// based on the installed plist and candidate bytes, not an official backup.
func prepareDarwinAgentASARIntegrityPatchFrom(target darwinTargets, candidateASAR, _ string) (*patchPlan, error) {
	plistPath, err := darwinAgentInfoPlistPath(target)
	if err != nil {
		return nil, err
	}
	candidateHash, err := darwinASARHeaderHash(candidateASAR)
	if err != nil {
		return nil, fmt.Errorf("计算候选 app.asar 完整性失败: %w", err)
	}
	data, err := os.ReadFile(plistPath)
	if err != nil {
		return nil, fmt.Errorf("读取 Antigravity 2.0 Info.plist 失败: %w", err)
	}
	declared, err := darwinElectronASARIntegrityHash(data)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(plistPath)
	if err != nil {
		return nil, err
	}
	if candidateHash == declared {
		return &patchPlan{path: plistPath, original: data, updated: append([]byte(nil), data...), mode: info.Mode()}, nil
	}

	// The parsed hierarchy proves which logical value owns the hash. Requiring
	// its exact bytes to occur once in the document makes the minimal in-place
	// replacement unambiguous and preserves all unrelated plist formatting.
	if bytes.Count(data, []byte(declared)) != 1 {
		return nil, errors.New("Info.plist 中 app.asar hash 出现多次；未修改任何文件")
	}
	updated := bytes.Replace(data, []byte(declared), []byte(candidateHash), 1)
	updatedHash, err := darwinElectronASARIntegrityHash(updated)
	if err != nil || updatedHash != candidateHash {
		if err != nil {
			return nil, fmt.Errorf("验证更新后的 Info.plist 失败: %w", err)
		}
		return nil, errors.New("更新后的 Info.plist app.asar hash 校验失败")
	}
	return &patchPlan{path: plistPath, original: data, updated: updated, mode: info.Mode(), changed: true}, nil
}

// verifyDarwinAgentASARIntegrity is intended for the end of the same
// transaction that replaces app.asar and writes the companion plist plan.
func verifyDarwinAgentASARIntegrity(target darwinTargets) error {
	return verifyDarwinAgentASARIntegrityAgainst(target, target.asar)
}

func verifyDarwinAgentASARIntegrityAgainst(target darwinTargets, asarPath string) error {
	plistPath, err := darwinAgentInfoPlistPath(target)
	if err != nil {
		return err
	}
	actual, err := darwinASARHeaderHash(asarPath)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(plistPath)
	if err != nil {
		return err
	}
	declared, err := darwinElectronASARIntegrityHash(data)
	if err != nil {
		return err
	}
	if declared != actual {
		return fmt.Errorf("Antigravity 2.0 app.asar 完整性校验失败: Info.plist=%s app.asar=%s", declared, actual)
	}
	return nil
}

func darwinElectronASARIntegrityHash(data []byte) (string, error) {
	trimmed := bytes.TrimSpace(data)
	if bytes.HasPrefix(trimmed, []byte("bplist00")) {
		return "", errors.New("二进制 Info.plist 尚未验证；未修改任何文件")
	}
	if !bytes.HasPrefix(trimmed, []byte("<?xml")) && !bytes.HasPrefix(trimmed, []byte("<plist")) {
		return "", errors.New("Info.plist 不是受支持的 XML plist；未修改任何文件")
	}
	root, err := parseDarwinXMLPlist(data)
	if err != nil {
		return "", fmt.Errorf("解析 Antigravity 2.0 XML Info.plist 失败: %w", err)
	}
	integrity, err := darwinUniquePlistDictValue(root, "ElectronAsarIntegrity")
	if err != nil {
		return "", err
	}
	archive, err := darwinUniquePlistDictValue(integrity, "Resources/app.asar")
	if err != nil {
		return "", err
	}
	algorithm, err := darwinUniquePlistStringValue(archive, "algorithm")
	if err != nil {
		return "", err
	}
	if algorithm != "SHA256" {
		return "", fmt.Errorf("ElectronAsarIntegrity 算法尚未验证: %q", algorithm)
	}
	hash, err := darwinUniquePlistStringValue(archive, "hash")
	if err != nil {
		return "", err
	}
	if len(hash) != sha256.Size*2 {
		return "", errors.New("ElectronAsarIntegrity hash 长度无效")
	}
	decoded, err := hex.DecodeString(hash)
	if err != nil || hex.EncodeToString(decoded) != hash {
		return "", errors.New("ElectronAsarIntegrity hash 必须为小写 SHA-256 十六进制")
	}
	return hash, nil
}

func darwinUniquePlistDictValue(value *darwinPlistValue, key string) (*darwinPlistValue, error) {
	if value == nil || value.kind != "dict" {
		return nil, fmt.Errorf("Info.plist 字段 %s 不在受验证的字典结构中", key)
	}
	var found *darwinPlistValue
	count := 0
	for _, entry := range value.entries {
		if entry.key == key {
			found = entry.value
			count++
		}
	}
	if count != 1 || found == nil || found.kind != "dict" {
		return nil, fmt.Errorf("Info.plist 字段 %s 必须是唯一字典", key)
	}
	return found, nil
}

func darwinUniquePlistStringValue(value *darwinPlistValue, key string) (string, error) {
	if value == nil || value.kind != "dict" {
		return "", fmt.Errorf("Info.plist 字段 %s 不在受验证的字典结构中", key)
	}
	var found *darwinPlistValue
	count := 0
	for _, entry := range value.entries {
		if entry.key == key {
			found = entry.value
			count++
		}
	}
	if count != 1 || found == nil || found.kind != "string" {
		return "", fmt.Errorf("Info.plist 字段 %s 必须是唯一字符串", key)
	}
	return found.text, nil
}

func parseDarwinXMLPlist(data []byte) (*darwinPlistValue, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	decoder.Strict = true
	for {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		if start.Name.Local != "plist" {
			return nil, fmt.Errorf("XML 根元素不是 plist: %s", start.Name.Local)
		}
		value, err := darwinReadPlistContainerValue(decoder, start)
		if err != nil {
			return nil, err
		}
		if value == nil || value.kind != "dict" {
			return nil, errors.New("Info.plist 根值不是字典")
		}
		for {
			tail, tailErr := decoder.Token()
			if tailErr == io.EOF {
				return value, nil
			}
			if tailErr != nil {
				return nil, tailErr
			}
			if chars, ok := tail.(xml.CharData); ok && len(bytes.TrimSpace(chars)) == 0 {
				continue
			}
			return nil, errors.New("Info.plist 末尾包含未识别内容")
		}
	}
}

func darwinReadPlistContainerValue(decoder *xml.Decoder, plistStart xml.StartElement) (*darwinPlistValue, error) {
	var value *darwinPlistValue
	for {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		switch typed := token.(type) {
		case xml.CharData:
			if len(bytes.TrimSpace(typed)) != 0 {
				return nil, errors.New("plist 容器中包含未识别文本")
			}
		case xml.StartElement:
			if value != nil {
				return nil, errors.New("plist 必须只包含一个根值")
			}
			value, err = darwinReadPlistValue(decoder, typed)
			if err != nil {
				return nil, err
			}
		case xml.EndElement:
			if typed.Name == plistStart.Name {
				if value == nil {
					return nil, errors.New("plist 缺少根值")
				}
				return value, nil
			}
		}
	}
}

func darwinReadPlistValue(decoder *xml.Decoder, start xml.StartElement) (*darwinPlistValue, error) {
	switch start.Name.Local {
	case "dict":
		return darwinReadPlistDict(decoder, start)
	case "array":
		return darwinReadPlistArray(decoder, start)
	case "string", "integer", "real", "date", "data", "true", "false":
		var builder strings.Builder
		for {
			token, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			switch typed := token.(type) {
			case xml.CharData:
				builder.Write([]byte(typed))
			case xml.StartElement:
				return nil, fmt.Errorf("plist %s 标量包含子元素", start.Name.Local)
			case xml.EndElement:
				if typed.Name == start.Name {
					text := builder.String()
					if start.Name.Local != "string" && start.Name.Local != "data" {
						text = strings.TrimSpace(text)
					}
					return &darwinPlistValue{kind: start.Name.Local, text: text}, nil
				}
			}
		}
	default:
		return nil, fmt.Errorf("Info.plist 包含未识别值类型: %s", start.Name.Local)
	}
}

func darwinReadPlistDict(decoder *xml.Decoder, start xml.StartElement) (*darwinPlistValue, error) {
	value := &darwinPlistValue{kind: "dict"}
	for {
		token, err := darwinNextPlistSignificantToken(decoder)
		if err != nil {
			return nil, err
		}
		if end, ok := token.(xml.EndElement); ok && end.Name == start.Name {
			return value, nil
		}
		keyStart, ok := token.(xml.StartElement)
		if !ok || keyStart.Name.Local != "key" {
			return nil, errors.New("plist 字典键结构无效")
		}
		key, err := darwinReadPlistKey(decoder, keyStart)
		if err != nil {
			return nil, err
		}
		next, err := darwinNextPlistSignificantToken(decoder)
		if err != nil {
			return nil, err
		}
		valueStart, ok := next.(xml.StartElement)
		if !ok {
			return nil, fmt.Errorf("plist 字典键 %s 缺少值", key)
		}
		child, err := darwinReadPlistValue(decoder, valueStart)
		if err != nil {
			return nil, err
		}
		value.entries = append(value.entries, darwinPlistEntry{key: key, value: child})
	}
}

func darwinReadPlistArray(decoder *xml.Decoder, start xml.StartElement) (*darwinPlistValue, error) {
	value := &darwinPlistValue{kind: "array"}
	for {
		token, err := darwinNextPlistSignificantToken(decoder)
		if err != nil {
			return nil, err
		}
		if end, ok := token.(xml.EndElement); ok && end.Name == start.Name {
			return value, nil
		}
		childStart, ok := token.(xml.StartElement)
		if !ok {
			return nil, errors.New("plist 数组值结构无效")
		}
		child, err := darwinReadPlistValue(decoder, childStart)
		if err != nil {
			return nil, err
		}
		value.values = append(value.values, child)
	}
}

func darwinReadPlistKey(decoder *xml.Decoder, start xml.StartElement) (string, error) {
	var builder strings.Builder
	for {
		token, err := decoder.Token()
		if err != nil {
			return "", err
		}
		switch typed := token.(type) {
		case xml.CharData:
			builder.Write([]byte(typed))
		case xml.StartElement:
			return "", errors.New("plist key 包含子元素")
		case xml.EndElement:
			if typed.Name == start.Name {
				return builder.String(), nil
			}
		}
	}
}

func darwinNextPlistSignificantToken(decoder *xml.Decoder) (xml.Token, error) {
	for {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		if chars, ok := token.(xml.CharData); ok && len(bytes.TrimSpace(chars)) == 0 {
			continue
		}
		return token, nil
	}
}
