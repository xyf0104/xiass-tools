package proxy

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"time"
)

const passthroughTimeout = 5 * time.Minute

// passthroughRequest forwards a request to the real Google API unchanged.
func passthroughRequest(w http.ResponseWriter, r *http.Request, body []byte, path string) {
	// Normalise path
	cleanPath := path
	if !strings.HasPrefix(cleanPath, "/") {
		cleanPath = "/" + cleanPath
	}

	targetURL := "https://daily-cloudcode-pa.googleapis.com" + cleanPath
	if r.URL.RawQuery != "" {
		targetURL += "?" + r.URL.RawQuery
	}

	req, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL, bytes.NewReader(body))
	if err != nil {
		http.Error(w, err.Error(), 502)
		return
	}

	// Forward all original headers except host
	for k, vs := range r.Header {
		if strings.ToLower(k) == "host" {
			continue
		}
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	req.Header.Set("Host", "daily-cloudcode-pa.googleapis.com")

	client := &http.Client{
		Timeout: passthroughTimeout,
		// Don't follow redirects automatically for SSE
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		trace("passthrough-error", map[string]any{
			"path":    cleanPath,
			"message": err.Error(),
		})
		http.Error(w, err.Error(), 502)
		return
	}
	defer resp.Body.Close()

	// Keep authentication diagnostics useful without ever logging request or
	// response bodies, authorization headers, cookies, or credential values.
	trace("passthrough-response", map[string]any{
		"path":       cleanPath,
		"statusCode": resp.StatusCode,
	})

	// Copy headers
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	written, err := io.Copy(w, resp.Body)
	if err != nil {
		trace("passthrough-stream-error", map[string]any{
			"path":                path,
			"message":             err.Error(),
			"downstreamCommitted": true,
			"downstreamBytes":     written,
		})
	}
}
