package mcpconfig

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"unicode/utf8"
)

const maxJSONDepth = 64

type configurationDocument struct {
	root                      map[string]json.RawMessage
	servers                   map[string]json.RawMessage
	hasSensitiveConfiguration bool
	managedServerConfigured   bool
}

func parseConfiguration(data []byte) (configurationDocument, error) {
	root, err := strictJSONObject(data)
	if err != nil {
		return configurationDocument{}, ErrInvalidConfiguration
	}
	document := configurationDocument{
		root:    root,
		servers: make(map[string]json.RawMessage),
	}
	if rawServers, exists := root["mcpServers"]; exists {
		servers, err := strictJSONObject(rawServers)
		if err != nil {
			return configurationDocument{}, ErrInvalidConfiguration
		}
		document.servers = servers
	}
	for key, raw := range root {
		if key == "mcpServers" {
			continue
		}
		if rawContainsSensitiveConfiguration(key, raw) {
			document.hasSensitiveConfiguration = true
		}
	}
	for id, raw := range document.servers {
		if _, err := strictJSONObject(raw); err != nil {
			return configurationDocument{}, ErrInvalidConfiguration
		}
		if id == ManagedServerID {
			document.managedServerConfigured = true
		}
		if rawContainsSensitiveConfiguration("", raw) || rawContainsSensitiveEndpoint(raw) {
			document.hasSensitiveConfiguration = true
		}
	}
	return document, nil
}

func (document configurationDocument) snapshot(target Target, exists bool) Snapshot {
	return Snapshot{
		Target:                    target,
		Exists:                    exists,
		Valid:                     true,
		ServerCount:               len(document.servers),
		ManagedServerConfigured:   document.managedServerConfigured,
		HasSensitiveConfiguration: document.hasSensitiveConfiguration,
	}
}

func (document configurationDocument) withManagedRemote(target Target, endpoint string) ([]byte, error) {
	if !target.valid() || document.root == nil || document.servers == nil {
		return nil, ErrOperation
	}
	field := "url"
	if target == TargetWindsurf {
		field = "serverUrl"
	}
	entry, err := json.Marshal(map[string]string{field: endpoint})
	if err != nil {
		return nil, ErrOperation
	}
	servers := make(map[string]json.RawMessage, len(document.servers)+1)
	for id, raw := range document.servers {
		servers[id] = append(json.RawMessage(nil), raw...)
	}
	servers[ManagedServerID] = entry
	encodedServers, err := json.Marshal(servers)
	if err != nil {
		return nil, ErrOperation
	}
	root := make(map[string]json.RawMessage, len(document.root)+1)
	for key, raw := range document.root {
		root[key] = append(json.RawMessage(nil), raw...)
	}
	root["mcpServers"] = encodedServers
	updated, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, ErrOperation
	}
	updated = append(updated, '\n')
	if _, err := parseConfiguration(updated); err != nil {
		return nil, ErrOperation
	}
	return updated, nil
}

// withoutManagedRemote removes only the exact reserved XIASS Tools key. It
// deliberately leaves the mcpServers container in place, including when it
// becomes empty, and preserves every foreign server plus every unknown
// top-level value. The caller must reject sensitive configurations before
// calling this method; this helper never attempts to classify another entry as
// owned by XIASS Tools.
func (document configurationDocument) withoutManagedRemote() ([]byte, bool, error) {
	if document.root == nil || document.servers == nil {
		return nil, false, ErrOperation
	}
	if !document.managedServerConfigured {
		return nil, false, nil
	}

	servers := make(map[string]json.RawMessage, len(document.servers)-1)
	for id, raw := range document.servers {
		if id == ManagedServerID {
			continue
		}
		servers[id] = append(json.RawMessage(nil), raw...)
	}
	encodedServers, err := json.Marshal(servers)
	if err != nil {
		return nil, false, ErrOperation
	}
	root := make(map[string]json.RawMessage, len(document.root))
	for key, raw := range document.root {
		root[key] = append(json.RawMessage(nil), raw...)
	}
	root["mcpServers"] = encodedServers
	updated, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, false, ErrOperation
	}
	updated = append(updated, '\n')
	if _, err := parseConfiguration(updated); err != nil {
		return nil, false, ErrOperation
	}
	return updated, true, nil
}

func (document configurationDocument) matchesManagedRemote(target Target, endpoint string) bool {
	raw, exists := document.servers[ManagedServerID]
	if !exists {
		return false
	}
	entry, err := strictJSONObject(raw)
	if err != nil {
		return false
	}
	field := "url"
	if target == TargetWindsurf {
		field = "serverUrl"
	}
	value, exists := entry[field]
	if !exists || len(entry) != 1 {
		return false
	}
	var configured string
	if json.Unmarshal(value, &configured) != nil {
		return false
	}
	return configured == endpoint
}

func strictJSONObject(data []byte) (map[string]json.RawMessage, error) {
	if len(data) == 0 || !utf8.Valid(data) {
		return nil, ErrInvalidConfiguration
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	rootObject, err := consumeStrictJSONValue(decoder, 0)
	if err != nil || !rootObject {
		return nil, ErrInvalidConfiguration
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, ErrInvalidConfiguration
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil || object == nil {
		return nil, ErrInvalidConfiguration
	}
	return object, nil
}

// consumeStrictJSONValue rejects duplicate object keys at every nesting level.
// encoding/json itself accepts duplicate keys, which would make a later write
// ambiguous and could silently replace a user-selected server definition.
func consumeStrictJSONValue(decoder *json.Decoder, depth int) (bool, error) {
	if depth > maxJSONDepth {
		return false, ErrInvalidConfiguration
	}
	token, err := decoder.Token()
	if err != nil {
		return false, ErrInvalidConfiguration
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		switch token.(type) {
		case string, bool, nil, json.Number:
			return false, nil
		default:
			return false, ErrInvalidConfiguration
		}
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			key, validKey := keyToken.(string)
			if err != nil || !validKey {
				return false, ErrInvalidConfiguration
			}
			if _, exists := seen[key]; exists {
				return false, ErrInvalidConfiguration
			}
			seen[key] = struct{}{}
			if _, err := consumeStrictJSONValue(decoder, depth+1); err != nil {
				return false, err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return false, ErrInvalidConfiguration
		}
		return true, nil
	case '[':
		for decoder.More() {
			if _, err := consumeStrictJSONValue(decoder, depth+1); err != nil {
				return false, err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return false, ErrInvalidConfiguration
		}
		return false, nil
	default:
		return false, ErrInvalidConfiguration
	}
}

func rawContainsSensitiveConfiguration(key string, raw json.RawMessage) bool {
	if sensitiveFieldName(key) {
		return true
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return true
	}
	return valueContainsSensitiveConfiguration(value)
}

func valueContainsSensitiveConfiguration(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, nested := range typed {
			if sensitiveFieldName(key) || valueContainsSensitiveConfiguration(nested) {
				return true
			}
		}
	case []any:
		for _, nested := range typed {
			if valueContainsSensitiveConfiguration(nested) {
				return true
			}
		}
	}
	return false
}

func sensitiveFieldName(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	if key == "" {
		return false
	}
	switch key {
	case "env", "headers", "auth", "envfile", "command", "args":
		return true
	}
	for _, marker := range []string{"token", "secret", "credential", "password", "cookie", "authorization", "api_key", "apikey"} {
		if strings.Contains(key, marker) {
			return true
		}
	}
	return false
}
