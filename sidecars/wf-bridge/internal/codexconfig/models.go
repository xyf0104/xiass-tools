package codexconfig

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	modelDiscoveryMaxModels = 256
	modelDiscoveryMaxBody   = 2 << 20
)

// ModelDiscoveryOptions permits a UI to use its own cancellable client and
// non-secret compatibility headers. Authorization is always supplied from the
// explicit API key and cannot be overridden by ExtraHeaders.
type ModelDiscoveryOptions struct {
	HTTPClient   *http.Client
	ExtraHeaders map[string]string
}

// DiscoverModels requests the standard OpenAI-compatible GET /models endpoint.
// The returned identifiers are de-duplicated, validated, and sorted.
func DiscoverModels(ctx context.Context, baseURL, apiKey string, options ModelDiscoveryOptions) ([]string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	normalized, err := NormalizeBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	if err := validateShortText(strings.TrimSpace(apiKey), 8192, "API key"); err != nil || strings.TrimSpace(apiKey) == "" {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("API key is required")
	}
	endpoint, err := url.Parse(normalized)
	if err != nil {
		return nil, errors.New("invalid normalized base URL")
	}
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/models"
	endpoint.RawQuery = ""
	endpoint.Fragment = ""

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create model discovery request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	for key, value := range options.ExtraHeaders {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || strings.EqualFold(key, "Authorization") || len(key) > 256 || len(value) > 4096 || strings.ContainsAny(key+value, "\r\n\x00") {
			return nil, errors.New("invalid model discovery header")
		}
		request.Header.Set(key, value)
	}
	client := options.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}
	clientCopy := *client
	clientCopy.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	response, err := clientCopy.Do(request)
	if err != nil {
		return nil, fmt.Errorf("request compatible API models: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("compatible API model list returned HTTP %d", response.StatusCode)
	}

	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, modelDiscoveryMaxBody)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode compatible API model list: %w", err)
	}
	modelSet := make(map[string]struct{}, len(payload.Data))
	for _, entry := range payload.Data {
		model := strings.TrimSpace(entry.ID)
		if err := validateShortText(model, 200, "model ID"); err != nil || model == "" {
			continue
		}
		modelSet[model] = struct{}{}
		if len(modelSet) > modelDiscoveryMaxModels {
			return nil, errors.New("compatible API returned too many models")
		}
	}
	if len(modelSet) == 0 {
		return nil, errors.New("compatible API did not return any usable model IDs")
	}
	models := make([]string, 0, len(modelSet))
	for model := range modelSet {
		models = append(models, model)
	}
	sort.Strings(models)
	return models, nil
}
