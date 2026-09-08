//go:build darwin

package patcher

// Antigravity's product.json tracks selected renderer resources independently
// of macOS code signing.  When a strictly recognised image renderer is
// changed, update only its matching checksum entry.  This prevents the IDE's
// own integrity layer from reporting a damaged installation while preserving
// every unrelated product.json byte and checksum.

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type darwinIDEProductIntegrity struct {
	Checksums map[string]string `json:"checksums"`
}

func darwinIDEProductPath(target darwinTargets) string {
	if target.kind != "ide" || target.app == "" {
		return ""
	}
	return filepath.Join(target.app, "Contents", "Resources", "app", "product.json")
}

func darwinIDEChecksum(data []byte) string {
	digest := sha256.Sum256(data)
	return base64.RawStdEncoding.EncodeToString(digest[:])
}

// prepareDarwinIDEProductChecksumPatch updates only tracked renderer entries.
// A checksum may belong to an older WF or third-party renderer; product.json
// itself is backed up from the active installation before the transaction.
func prepareDarwinIDEProductChecksumPatch(target darwinTargets, rendererPlans []*patchPlan) (*patchPlan, error) {
	productPath := darwinIDEProductPath(target)
	if productPath == "" || existingFile(productPath) == "" {
		return nil, nil
	}
	data, err := os.ReadFile(productPath)
	if err != nil {
		return nil, err
	}
	var product darwinIDEProductIntegrity
	if err := json.Unmarshal(data, &product); err != nil {
		return nil, fmt.Errorf("解析 IDE product.json 完整性配置失败: %w", err)
	}
	if len(product.Checksums) == 0 {
		return nil, nil
	}

	appOut := filepath.Join(target.app, "Contents", "Resources", "app", "out")
	updated := string(data)
	changed := false
	for _, plan := range rendererPlans {
		if plan == nil || plan.path == "" || !plan.changed {
			continue
		}
		relative, relErr := filepath.Rel(appOut, plan.path)
		if relErr != nil || relative == "." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) || relative == ".." {
			continue
		}
		key := filepath.ToSlash(relative)
		current, tracked := product.Checksums[key]
		if !tracked {
			continue
		}
		desired := darwinIDEChecksum(plan.updated)
		if current == desired {
			continue
		}
		pattern := regexp.MustCompile(`("` + regexp.QuoteMeta(key) + `"\s*:\s*")` + regexp.QuoteMeta(current) + `(")`)
		if len(pattern.FindAllStringIndex(updated, -1)) != 1 {
			return nil, fmt.Errorf("IDE checksum 字段 %s 的结构尚未验证；未修改任何文件", key)
		}
		updated = pattern.ReplaceAllString(updated, `${1}`+desired+`${2}`)
		product.Checksums[key] = desired
		changed = true
	}
	if !changed {
		return nil, nil
	}
	info, err := os.Stat(productPath)
	if err != nil {
		return nil, err
	}
	return &patchPlan{path: productPath, original: data, updated: []byte(updated), mode: info.Mode(), changed: true}, nil
}

func verifyDarwinIDEProductChecksums(target darwinTargets, rendererPaths []string) error {
	productPath := darwinIDEProductPath(target)
	if productPath == "" || existingFile(productPath) == "" {
		return nil
	}
	data, err := os.ReadFile(productPath)
	if err != nil {
		return err
	}
	var product darwinIDEProductIntegrity
	if err := json.Unmarshal(data, &product); err != nil {
		return err
	}
	if len(product.Checksums) == 0 {
		return nil
	}
	appOut := filepath.Join(target.app, "Contents", "Resources", "app", "out")
	for _, path := range rendererPaths {
		relative, relErr := filepath.Rel(appOut, path)
		if relErr != nil || relative == "." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) || relative == ".." {
			continue
		}
		key := filepath.ToSlash(relative)
		expected, tracked := product.Checksums[key]
		if !tracked {
			continue
		}
		actual, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if darwinIDEChecksum(actual) != expected {
			return fmt.Errorf("IDE renderer checksum 校验失败: %s", key)
		}
	}
	return nil
}
