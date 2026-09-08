package proxy

import (
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Antigravity sends attachments as Gemini inlineData or fileData parts. The
// proxy deliberately limits local reads to files the user explicitly attached
// (file:// URIs), never follows remote URLs, and keeps each payload inside the
// upstream Responses API's documented per-request envelope.
const maxForwardedAttachmentBytes int64 = 50 << 20

type geminiAttachment struct {
	MimeType string
	Data     string // base64 without a data: prefix
	Filename string
}

func getString(value map[string]any, names ...string) string {
	for _, name := range names {
		if result, ok := value[name].(string); ok && strings.TrimSpace(result) != "" {
			return strings.TrimSpace(result)
		}
	}
	return ""
}

// attachmentFromGeminiPart returns seen=false for ordinary text/tool parts.
// A seen attachment that cannot be read is an error: silently dropping a user
// screenshot or file produces misleading model answers.
func attachmentFromGeminiPart(part map[string]any) (*geminiAttachment, bool, error) {
	inlineData, hasInline := part["inlineData"].(map[string]any)
	if !hasInline {
		inlineData, hasInline = part["inline_data"].(map[string]any)
	}
	if hasInline {
		mimeType := getString(inlineData, "mimeType", "mime_type")
		data := getString(inlineData, "data")
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}
		base64Data, detectedMime, err := normaliseAttachmentData(data, mimeType)
		if err != nil {
			return nil, true, fmt.Errorf("无法读取内嵌附件：%w", err)
		}
		if detectedMime != "" {
			mimeType = detectedMime
		}
		return &geminiAttachment{
			MimeType: normaliseMimeType(mimeType),
			Data:     base64Data,
			Filename: getString(inlineData, "displayName", "display_name", "fileName", "filename", "name"),
		}, true, nil
	}

	fileData, hasFile := part["fileData"].(map[string]any)
	if !hasFile {
		fileData, hasFile = part["file_data"].(map[string]any)
	}
	if !hasFile {
		return nil, false, nil
	}

	mimeType := getString(fileData, "mimeType", "mime_type")
	filename := getString(fileData, "displayName", "display_name", "fileName", "filename", "name")
	if data := getString(fileData, "data"); data != "" {
		base64Data, detectedMime, err := normaliseAttachmentData(data, mimeType)
		if err != nil {
			return nil, true, fmt.Errorf("无法读取文件附件：%w", err)
		}
		if detectedMime != "" {
			mimeType = detectedMime
		}
		return &geminiAttachment{MimeType: normaliseMimeType(mimeType), Data: base64Data, Filename: filename}, true, nil
	}

	uri := getString(fileData, "fileUri", "file_uri", "uri")
	if uri == "" {
		return nil, true, fmt.Errorf("文件附件未提供可读取的数据或 file URI")
	}
	if strings.HasPrefix(strings.ToLower(uri), "data:") {
		base64Data, detectedMime, err := normaliseAttachmentData(uri, mimeType)
		if err != nil {
			return nil, true, fmt.Errorf("无法读取数据 URI 附件：%w", err)
		}
		if detectedMime != "" {
			mimeType = detectedMime
		}
		return &geminiAttachment{MimeType: normaliseMimeType(mimeType), Data: base64Data, Filename: filename}, true, nil
	}

	parsed, err := url.Parse(uri)
	if err != nil || parsed.Scheme != "file" {
		return nil, true, fmt.Errorf("暂不支持转发此文件 URI；请重新以本地文件或截图附加")
	}
	if parsed.Host != "" && !strings.EqualFold(parsed.Host, "localhost") {
		return nil, true, fmt.Errorf("不允许读取非本机 file URI")
	}
	filePath, err := url.PathUnescape(parsed.Path)
	if err != nil || filePath == "" {
		return nil, true, fmt.Errorf("本地文件路径无效")
	}
	info, err := os.Stat(filePath)
	if err != nil || !info.Mode().IsRegular() {
		return nil, true, fmt.Errorf("无法读取已附加的本地文件")
	}
	if info.Size() > maxForwardedAttachmentBytes {
		return nil, true, fmt.Errorf("附件超过 %d MB 上限", maxForwardedAttachmentBytes>>20)
	}
	file, err := os.Open(filePath)
	if err != nil {
		return nil, true, fmt.Errorf("无法打开已附加的本地文件")
	}
	defer file.Close()
	bytes, err := io.ReadAll(io.LimitReader(file, maxForwardedAttachmentBytes+1))
	if err != nil || int64(len(bytes)) > maxForwardedAttachmentBytes {
		return nil, true, fmt.Errorf("无法完整读取已附加的本地文件")
	}
	if filename == "" {
		filename = filepath.Base(filePath)
	}
	if mimeType == "" {
		mimeType = mime.TypeByExtension(strings.ToLower(filepath.Ext(filename)))
	}
	return &geminiAttachment{
		MimeType: normaliseMimeType(mimeType),
		Data:     base64.StdEncoding.EncodeToString(bytes),
		Filename: filename,
	}, true, nil
}

func normaliseAttachmentData(raw, fallbackMime string) (string, string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", fmt.Errorf("附件内容为空")
	}
	if strings.HasPrefix(strings.ToLower(raw), "data:") {
		comma := strings.IndexByte(raw, ',')
		if comma < 0 {
			return "", "", fmt.Errorf("数据 URI 格式无效")
		}
		meta, payload := raw[5:comma], raw[comma+1:]
		mimeType := fallbackMime
		if semicolon := strings.IndexByte(meta, ';'); semicolon >= 0 {
			if strings.TrimSpace(meta[:semicolon]) != "" {
				mimeType = meta[:semicolon]
			}
		} else if strings.TrimSpace(meta) != "" {
			mimeType = meta
		}
		if !strings.Contains(strings.ToLower(meta), ";base64") {
			decoded, err := url.QueryUnescape(payload)
			if err != nil {
				return "", "", fmt.Errorf("数据 URI 编码无效")
			}
			if int64(len(decoded)) > maxForwardedAttachmentBytes {
				return "", "", fmt.Errorf("附件超过 %d MB 上限", maxForwardedAttachmentBytes>>20)
			}
			return base64.StdEncoding.EncodeToString([]byte(decoded)), mimeType, nil
		}
		raw = payload
		fallbackMime = mimeType
	}
	clean := strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' || r == ' ' {
			return -1
		}
		return r
	}, raw)
	decoded, err := base64.StdEncoding.DecodeString(clean)
	if err != nil {
		return "", "", fmt.Errorf("Base64 编码无效")
	}
	if int64(len(decoded)) > maxForwardedAttachmentBytes {
		return "", "", fmt.Errorf("附件超过 %d MB 上限", maxForwardedAttachmentBytes>>20)
	}
	return clean, fallbackMime, nil
}

func normaliseMimeType(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if semicolon := strings.IndexByte(value, ';'); semicolon >= 0 {
		value = strings.TrimSpace(value[:semicolon])
	}
	if value == "" || !strings.Contains(value, "/") {
		return "application/octet-stream"
	}
	return value
}

func (attachment geminiAttachment) dataURL() string {
	return "data:" + attachment.MimeType + ";base64," + attachment.Data
}

func (attachment geminiAttachment) isImage() bool {
	return strings.HasPrefix(attachment.MimeType, "image/")
}

func (attachment geminiAttachment) isPDF() bool {
	return attachment.MimeType == "application/pdf"
}

func (attachment geminiAttachment) isText() bool {
	return strings.HasPrefix(attachment.MimeType, "text/") ||
		strings.Contains(attachment.MimeType, "json") ||
		strings.Contains(attachment.MimeType, "xml") ||
		strings.Contains(attachment.MimeType, "javascript") ||
		strings.Contains(attachment.MimeType, "typescript") ||
		strings.Contains(attachment.MimeType, "python")
}

func (attachment geminiAttachment) text() (string, error) {
	decoded, err := base64.StdEncoding.DecodeString(attachment.Data)
	if err != nil {
		return "", fmt.Errorf("文本附件解码失败")
	}
	return string(decoded), nil
}

func hasGeminiAttachment(gemini map[string]any) bool {
	contents, _ := gemini["contents"].([]any)
	for _, content := range contents {
		message, _ := content.(map[string]any)
		parts, _ := message["parts"].([]any)
		for _, rawPart := range parts {
			part, _ := rawPart.(map[string]any)
			if _, ok := part["inlineData"]; ok {
				return true
			}
			if _, ok := part["inline_data"]; ok {
				return true
			}
			if _, ok := part["fileData"]; ok {
				return true
			}
			if _, ok := part["file_data"]; ok {
				return true
			}
		}
	}
	return false
}
