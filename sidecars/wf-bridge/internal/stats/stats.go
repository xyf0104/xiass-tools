package stats

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Stats aggregates usage from proxy-trace.jsonl.
type Stats struct {
	TotalRequests    int     `json:"totalRequests"`
	CustomRequests   int     `json:"customRequests"`
	TotalTokens      int     `json:"totalTokens"`
	PromptTokens     int     `json:"promptTokens"`
	CompletionTokens int     `json:"completionTokens"`
	CacheReadTokens  int     `json:"cacheReadTokens"`
	CacheWriteTokens int     `json:"cacheWriteTokens"`
	CacheHitRate     float64 `json:"cacheHitRate"`
}

// AccountUsage is the locally observed, token-bearing usage for one bound
// upstream account. It does not infer billing or quota from a normal API key.
type AccountUsage struct {
	AccountID        string `json:"accountId"`
	RequestCount     int    `json:"requestCount"`
	TotalTokens      int    `json:"totalTokens"`
	PromptTokens     int    `json:"promptTokens"`
	CompletionTokens int    `json:"completionTokens"`
	CacheReadTokens  int    `json:"cacheReadTokens"`
	CacheWriteTokens int    `json:"cacheWriteTokens"`
	LastUsedAt       string `json:"lastUsedAt,omitempty"`
}

// Compute reads proxy-trace.jsonl and returns aggregated stats.
func Compute(storageDir string) Stats {
	path := filepath.Join(storageDir, "proxy-trace.jsonl")
	f, err := os.Open(path)
	if err != nil {
		return Stats{}
	}
	defer f.Close()

	var s Stats
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 2*1024*1024), 2*1024*1024)
	for scanner.Scan() {
		var ev map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			continue
		}
		evType, _ := ev["event"].(string)
		switch evType {
		case "generation-request":
			s.TotalRequests++
			if matched, _ := ev["customMatched"].(bool); matched {
				s.CustomRequests++
			}
		case "usage":
			if p, ok := ev["promptTokens"].(float64); ok {
				s.PromptTokens += int(p)
				s.TotalTokens += int(p)
			}
			if c, ok := ev["completionTokens"].(float64); ok {
				s.CompletionTokens += int(c)
				s.TotalTokens += int(c)
			}
			if cr, ok := ev["cacheReadTokens"].(float64); ok {
				s.CacheReadTokens += int(cr)
			}
			if cw, ok := ev["cacheWriteTokens"].(float64); ok {
				s.CacheWriteTokens += int(cw)
			}
		}
	}

	// Calculate cache hit rate: cacheRead / (cacheRead + non-cache input)
	nonCacheInput := s.PromptTokens - s.CacheReadTokens - s.CacheWriteTokens
	if nonCacheInput < 0 {
		nonCacheInput = 0
	}
	denom := s.CacheReadTokens + nonCacheInput
	if denom > 0 {
		s.CacheHitRate = float64(s.CacheReadTokens) / float64(denom)
	}

	return s
}

// ComputeAccountUsage aggregates local usage by the account selected for an
// upstream stream. Legacy models without an account binding are intentionally
// excluded because their token events have no stable account identity.
func ComputeAccountUsage(storageDir string) map[string]AccountUsage {
	path := filepath.Join(storageDir, "proxy-trace.jsonl")
	f, err := os.Open(path)
	if err != nil {
		return map[string]AccountUsage{}
	}
	defer f.Close()

	usageByAccount := make(map[string]AccountUsage)
	seenAttempts := make(map[string]struct{})
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 2*1024*1024), 2*1024*1024)
	for scanner.Scan() {
		var event map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}
		if eventType, _ := event["event"].(string); eventType != "usage" {
			continue
		}

		accountID := strings.TrimSpace(traceString(event["accountId"]))
		if accountID == "" {
			continue
		}
		if key := accountUsageAttemptKey(accountID, event); key != "" {
			if _, duplicate := seenAttempts[key]; duplicate {
				continue
			}
			seenAttempts[key] = struct{}{}
		}

		usage := usageByAccount[accountID]
		if usage.AccountID == "" {
			usage.AccountID = accountID
		}
		usage.RequestCount++
		usage.PromptTokens += traceInt(event["promptTokens"])
		usage.CompletionTokens += traceInt(event["completionTokens"])
		usage.CacheReadTokens += traceInt(event["cacheReadTokens"])
		usage.CacheWriteTokens += traceInt(event["cacheWriteTokens"])
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
		if usedAt := strings.TrimSpace(traceString(event["time"])); isLaterTraceTime(usage.LastUsedAt, usedAt) {
			usage.LastUsedAt = usedAt
		}
		usageByAccount[accountID] = usage
	}

	return usageByAccount
}

func accountUsageAttemptKey(accountID string, event map[string]any) string {
	requestID := strings.TrimSpace(traceString(event["requestId"]))
	attempt, ok := traceInteger(event["attempt"])
	if requestID == "" || !ok {
		return ""
	}
	return accountID + "\x00" + requestID + "\x00" + strconv.Itoa(attempt)
}

func traceString(value any) string {
	text, _ := value.(string)
	return text
}

func traceInt(value any) int {
	integer, _ := traceInteger(value)
	return integer
}

func traceInteger(value any) (int, bool) {
	switch value := value.(type) {
	case int:
		return value, true
	case int64:
		return int(value), true
	case float64:
		return int(value), true
	case json.Number:
		integer, err := value.Int64()
		return int(integer), err == nil
	default:
		return 0, false
	}
}

func isLaterTraceTime(current, candidate string) bool {
	if candidate == "" {
		return false
	}
	if current == "" {
		return true
	}
	currentTime, currentErr := time.Parse(time.RFC3339Nano, current)
	candidateTime, candidateErr := time.Parse(time.RFC3339Nano, candidate)
	if currentErr == nil && candidateErr == nil {
		return candidateTime.After(currentTime)
	}
	return candidate > current
}
