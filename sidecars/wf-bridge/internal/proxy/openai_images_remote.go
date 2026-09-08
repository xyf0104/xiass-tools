package proxy

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// openAIImageRemoteURL extracts the URL fallback used by compatibility
// gateways which ignore response_format=b64_json. Data URLs are handled by
// openAIImageDataFromResponse; only HTTPS reaches the downloader below.
func openAIImageRemoteURL(body []byte) string {
	var payload map[string]any
	if json.Unmarshal(body, &payload) != nil {
		return ""
	}
	for _, key := range []string{"data", "images", "output", "results"} {
		items, _ := payload[key].([]any)
		for _, rawItem := range items {
			item, _ := rawItem.(map[string]any)
			if value := imageRemoteURLFromValue(item); value != "" {
				return value
			}
		}
	}
	return imageRemoteURLFromValue(payload)
}

func imageRemoteURLFromValue(value map[string]any) string {
	for _, key := range []string{"url", "image_url", "imageUrl"} {
		raw, _ := value[key].(string)
		raw = strings.TrimSpace(raw)
		if strings.HasPrefix(strings.ToLower(raw), "https://") {
			return raw
		}
	}
	return ""
}

func downloadOpenAIImage(ctx context.Context, rawURL string) (string, string, error) {
	client := &http.Client{
		Transport:     remoteImageTransport(),
		Timeout:       45 * time.Second,
		CheckRedirect: validateRemoteImageRedirect,
	}
	return downloadOpenAIImageWithClient(ctx, rawURL, client)
}

func downloadOpenAIImageWithClient(ctx context.Context, rawURL string, client *http.Client) (string, string, error) {
	if _, err := validateRemoteImageURL(rawURL); err != nil {
		return "", "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", "", fmt.Errorf("图片下载地址无效")
	}
	req.Header.Set("Accept", "image/png,image/jpeg,image/webp,image/gif")
	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("无法下载上游生成的图片")
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", "", fmt.Errorf("上游图片下载返回 HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxForwardedAttachmentBytes+1))
	if err != nil || int64(len(body)) > maxForwardedAttachmentBytes {
		return "", "", fmt.Errorf("上游图片无法读取或超过 %d MB 上限", maxForwardedAttachmentBytes>>20)
	}
	contentType := normaliseMimeType(resp.Header.Get("Content-Type"))
	if !strings.HasPrefix(contentType, "image/") {
		contentType = normaliseMimeType(http.DetectContentType(body))
	}
	if !strings.HasPrefix(contentType, "image/") {
		return "", "", fmt.Errorf("上游下载结果不是图片")
	}
	return base64.StdEncoding.EncodeToString(body), directImageMimeType(contentType), nil
}

func validateRemoteImageURL(rawURL string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || !strings.EqualFold(parsed.Scheme, "https") || parsed.Hostname() == "" || parsed.User != nil {
		return nil, fmt.Errorf("只允许下载上游返回的 HTTPS 图片")
	}
	return parsed, nil
}

func validateRemoteImageRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 3 {
		return fmt.Errorf("上游图片重定向次数过多")
	}
	_, err := validateRemoteImageURL(req.URL.String())
	return err
}

func remoteImageTransport() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 15 * time.Second}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("图片下载地址无效")
		}
		addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil || len(addresses) == 0 {
			return nil, fmt.Errorf("无法解析图片下载地址")
		}
		for _, candidate := range addresses {
			if !isPublicRemoteImageIP(candidate.IP) {
				return nil, fmt.Errorf("图片下载地址指向受保护的本机或内网地址")
			}
		}
		return dialer.DialContext(ctx, network, net.JoinHostPort(addresses[0].IP.String(), port))
	}
	return transport
}

func isPublicRemoteImageIP(ip net.IP) bool {
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
		return false
	}
	if _, shared, err := net.ParseCIDR("100.64.0.0/10"); err == nil && shared.Contains(ip) {
		return false
	}
	return true
}
