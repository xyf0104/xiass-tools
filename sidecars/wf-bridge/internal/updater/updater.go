// Package updater performs a deliberately narrow, verified update check for
// XIASS Tools' own public GitHub releases. It never accepts an
// arbitrary URL from the renderer and verifies the published SHA256 manifest
// before handing an installer to the operating system.
package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	Repository     = "xyf0104/Antigravity-WF-Assistant"
	CurrentVersion = "1.7.5"
	maxAssetBytes  = int64(2 << 30) // installers are normally tens of MB
	// CheckTimeout keeps a background update check from blocking the UI when a
	// network, DNS resolver, proxy, or captive portal is unhealthy.
	CheckTimeout = 5 * time.Second
	// FreshCacheTTL lets repeat checks return immediately from the last verified
	// release metadata instead of holding the UI open for another network call.
	// Expired entries still make a conditional request with their ETag.
	FreshCacheTTL = 10 * time.Minute
)

// githubLatestReleaseURL is deliberately kept as the package's historical
// test hook. It now points at the releases collection rather than GitHub's
// /releases/latest endpoint: GitHub defines "latest" by publication time,
// which can point at an older version after a release is republished.
var githubLatestReleaseURL = "https://api.github.com/repos/" + Repository + "/releases?per_page=100"

type githubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

type githubRelease struct {
	TagName     string        `json:"tag_name"`
	HTMLURL     string        `json:"html_url"`
	Body        string        `json:"body"`
	PublishedAt string        `json:"published_at"`
	Prerelease  bool          `json:"prerelease"`
	Draft       bool          `json:"draft"`
	Assets      []githubAsset `json:"assets"`
}

// Info is renderer-safe metadata. AssetURL is intentionally omitted; the
// install method resolves the asset from GitHub again before downloading it.
type Info struct {
	CurrentVersion string `json:"currentVersion"`
	LatestVersion  string `json:"latestVersion"`
	Available      bool   `json:"available"`
	Skipped        bool   `json:"skipped"`
	ReleaseURL     string `json:"releaseUrl"`
	AssetName      string `json:"assetName"`
	AssetSize      int64  `json:"assetSize"`
	PublishedAt    string `json:"publishedAt"`
	Notes          string `json:"notes"`
	// Cached is true when last verified release metadata is being shown either
	// immediately from a fresh cache or as a network fallback. Downloads never
	// trust it: they always fetch the release and checksum again.
	Cached      bool   `json:"cached"`
	CacheReason string `json:"cacheReason,omitempty"`
	CheckedAt   string `json:"checkedAt"`
}

func Check(ctx context.Context, skippedVersion string) (Info, error) {
	return CheckWithCache(ctx, skippedVersion, "")
}

// CheckWithCache returns a recent verified cache immediately. Once it expires,
// it performs one short conditional release request using the cached ETag so
// GitHub can reply 304 without sending release JSON. If that request fails,
// the stale cache is an explicitly marked display fallback. cachePath may be
// empty for callers that do not need disk caching (for example unit tests).
func CheckWithCache(ctx context.Context, skippedVersion, cachePath string) (Info, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, CheckTimeout)
	defer cancel()
	release, cached, cacheReason, checkedAt, err := latestReleaseWithCache(ctx, cachePath)
	if err != nil {
		return Info{CurrentVersion: CurrentVersion}, err
	}
	info, err := infoForRelease(release, skippedVersion, runtime.GOOS, cached, checkedAt)
	info.CacheReason = cacheReason
	return info, err
}

func infoForRelease(release githubRelease, skippedVersion, platform string, cached bool, checkedAt string) (Info, error) {
	latest := normalizeVersion(release.TagName)
	if latest == "" {
		return Info{CurrentVersion: CurrentVersion}, fmt.Errorf("更新源返回了无效版本号")
	}
	asset, err := selectInstaller(release, platform)
	if err != nil {
		return Info{CurrentVersion: CurrentVersion, LatestVersion: latest, ReleaseURL: release.HTMLURL, Cached: cached, CheckedAt: checkedAt}, err
	}
	available := compareVersions(latest, CurrentVersion) > 0
	return Info{
		CurrentVersion: CurrentVersion,
		LatestVersion:  latest,
		Available:      available,
		Skipped:        available && normalizeVersion(skippedVersion) == latest,
		ReleaseURL:     release.HTMLURL,
		AssetName:      asset.Name,
		AssetSize:      asset.Size,
		PublishedAt:    release.PublishedAt,
		Notes:          truncateNotes(release.Body),
		Cached:         cached,
		CheckedAt:      checkedAt,
	}, nil
}

// DownloadLatestInstaller downloads the newest matching installer only after
// reconfirming it is newer than this build and validating its SHA256 against
// the release manifest. report may be nil.
func DownloadLatestInstaller(ctx context.Context, report func(downloaded, total int64)) (string, Info, error) {
	release, err := latestRelease(ctx)
	if err != nil {
		return "", Info{CurrentVersion: CurrentVersion}, err
	}
	latest := normalizeVersion(release.TagName)
	if compareVersions(latest, CurrentVersion) <= 0 {
		return "", Info{CurrentVersion: CurrentVersion, LatestVersion: latest}, fmt.Errorf("当前已是最新版本")
	}
	asset, err := selectInstaller(release, runtime.GOOS)
	if err != nil {
		return "", Info{CurrentVersion: CurrentVersion, LatestVersion: latest, ReleaseURL: release.HTMLURL}, err
	}
	if asset.Size <= 0 || asset.Size > maxAssetBytes {
		return "", Info{}, fmt.Errorf("更新安装包大小异常")
	}
	expected, err := releaseChecksum(ctx, release, asset.Name)
	if err != nil {
		return "", Info{}, err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.BrowserDownloadURL, nil)
	if err != nil {
		return "", Info{}, fmt.Errorf("无法创建安装包下载请求")
	}
	request.Header.Set("User-Agent", "XIASS-Tools/"+CurrentVersion)
	response, err := httpClient().Do(request)
	if err != nil {
		return "", Info{}, fmt.Errorf("下载更新失败")
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return "", Info{}, fmt.Errorf("下载更新失败（HTTP %d）", response.StatusCode)
	}
	if response.ContentLength > maxAssetBytes {
		return "", Info{}, fmt.Errorf("更新安装包过大，已拒绝下载")
	}

	temp, err := os.CreateTemp("", "xiass-tools-update-*."+installerExtension(asset.Name))
	if err != nil {
		return "", Info{}, err
	}
	path := temp.Name()
	success := false
	defer func() {
		if !success {
			_ = temp.Close()
			_ = os.Remove(path)
		}
	}()

	hash := sha256.New()
	reader := io.LimitReader(response.Body, maxAssetBytes+1)
	buffer := make([]byte, 128*1024)
	var downloaded int64
	for {
		read, readErr := reader.Read(buffer)
		if read > 0 {
			downloaded += int64(read)
			if downloaded > maxAssetBytes {
				return "", Info{}, fmt.Errorf("更新安装包过大，已拒绝下载")
			}
			if _, err := hash.Write(buffer[:read]); err != nil {
				return "", Info{}, err
			}
			if _, err := temp.Write(buffer[:read]); err != nil {
				return "", Info{}, err
			}
			if report != nil {
				report(downloaded, asset.Size)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", Info{}, fmt.Errorf("读取更新安装包失败")
		}
	}
	if asset.Size > 0 && downloaded != asset.Size {
		return "", Info{}, fmt.Errorf("更新安装包大小校验失败")
	}
	if got := hex.EncodeToString(hash.Sum(nil)); !strings.EqualFold(got, expected) {
		return "", Info{}, fmt.Errorf("更新安装包校验失败，已丢弃下载文件")
	}
	if err := temp.Close(); err != nil {
		return "", Info{}, err
	}
	success = true
	return path, Info{
		CurrentVersion: CurrentVersion, LatestVersion: latest, Available: true,
		ReleaseURL: release.HTMLURL, AssetName: asset.Name, AssetSize: asset.Size,
		PublishedAt: release.PublishedAt, Notes: truncateNotes(release.Body),
	}, nil
}

const releaseCacheFormat = 2

type releaseCache struct {
	// Format 2 stores the full GitHub release-list response. Release is kept
	// solely so version-1 caches can be read as a safe, stale fallback while a
	// fresh list is fetched. Old ETags belong to /releases/latest and are never
	// sent to the new /releases endpoint.
	Format    int             `json:"format,omitempty"`
	ETag      string          `json:"etag,omitempty"`
	CheckedAt string          `json:"checkedAt,omitempty"`
	Releases  []githubRelease `json:"releases,omitempty"`
	Release   githubRelease   `json:"release,omitempty"`
}

// latestRelease remains a small compatibility wrapper for callers and tests
// that used the old helper. Both checking and downloading now resolve their
// release through the same platform-aware semantic-version selection path.
func latestRelease(ctx context.Context) (githubRelease, error) {
	return latestReleaseForPlatform(ctx, runtime.GOOS)
}

func latestReleaseForPlatform(ctx context.Context, platform string) (githubRelease, error) {
	releases, _, _, err := fetchReleaseList(ctx, "")
	if err != nil {
		return githubRelease{}, err
	}
	return selectHighestStableRelease(releases, platform)
}

func latestReleaseWithCache(ctx context.Context, cachePath string) (githubRelease, bool, string, string, error) {
	return latestReleaseWithCacheForPlatform(ctx, cachePath, runtime.GOOS)
}

func latestReleaseWithCacheForPlatform(ctx context.Context, cachePath, platform string) (githubRelease, bool, string, string, error) {
	cache, cacheOK := loadReleaseCache(cachePath)
	cachedReleases, cachedReleasesOK := releaseListFromCache(cache)
	cacheOK = cacheOK && cachedReleasesOK
	// A caller cancellation always wins, including when a fresh cache exists.
	// Otherwise clicking “取消检查” could misleadingly look successful.
	if err := ctx.Err(); err != nil {
		return githubRelease{}, false, "", "", err
	}
	// A version-1 cache only contains the answer from /releases/latest, so it
	// cannot safely be considered fresh: it may have hidden a higher semantic
	// version. It is retained only as an offline fallback until migrated.
	if cacheOK && !isLegacyReleaseCache(cache) && isFreshReleaseCache(cache, time.Now()) {
		release, err := selectHighestStableRelease(cachedReleases, platform)
		if err != nil {
			return githubRelease{}, false, "", "", err
		}
		return release, true, "fresh", cache.CheckedAt, nil
	}

	etag := ""
	if cacheOK && !isLegacyReleaseCache(cache) {
		etag = cache.ETag
	}
	releases, receivedETag, notModified, err := fetchReleaseList(ctx, etag)
	if err != nil {
		// A user explicitly canceled this request. Returning cached data here
		// would make a cancelled check look as if it had completed normally.
		if errors.Is(err, context.Canceled) {
			return githubRelease{}, false, "", "", err
		}
		if cacheOK {
			release, cachedErr := selectHighestStableRelease(cachedReleases, platform)
			if cachedErr == nil {
				reason := "network"
				if errors.Is(err, context.DeadlineExceeded) {
					reason = "timeout"
				}
				return release, true, reason, cache.CheckedAt, nil
			}
		}
		return githubRelease{}, false, "", "", err
	}

	checkedAt := time.Now().UTC().Format(time.RFC3339)
	if notModified {
		if !cacheOK || isLegacyReleaseCache(cache) {
			return githubRelease{}, false, "", "", fmt.Errorf("GitHub 返回了缓存状态，但本地更新缓存不可用；请重试")
		}
		release, err := selectHighestStableRelease(cachedReleases, platform)
		if err != nil {
			return githubRelease{}, false, "", "", err
		}
		cache.CheckedAt = checkedAt
		if receivedETag != "" {
			cache.ETag = receivedETag
		}
		_ = saveReleaseCache(cachePath, cache)
		return release, false, "", checkedAt, nil
	}

	// Persist the entire list before selecting from it. This makes a later 304
	// response deterministic and migrates legacy single-release caches.
	cache = releaseCache{Format: releaseCacheFormat, ETag: receivedETag, CheckedAt: checkedAt, Releases: releases}
	_ = saveReleaseCache(cachePath, cache)
	release, err := selectHighestStableRelease(releases, platform)
	if err != nil {
		return githubRelease{}, false, "", "", err
	}
	return release, false, "", checkedAt, nil
}

func isFreshReleaseCache(cache releaseCache, now time.Time) bool {
	if FreshCacheTTL <= 0 {
		return false
	}
	checkedAt, err := time.Parse(time.RFC3339, strings.TrimSpace(cache.CheckedAt))
	if err != nil || checkedAt.After(now) {
		return false
	}
	return now.Sub(checkedAt) < FreshCacheTTL
}

// fetchLatestRelease is retained for package-level compatibility. New update
// flows use fetchReleaseList so cached ETag responses still have a release
// collection from which to resolve the same highest version.
func fetchLatestRelease(ctx context.Context, etag string) (githubRelease, string, bool, error) {
	releases, responseETag, notModified, err := fetchReleaseList(ctx, etag)
	if err != nil || notModified {
		return githubRelease{}, responseETag, notModified, err
	}
	release, err := selectHighestStableRelease(releases, runtime.GOOS)
	return release, responseETag, false, err
}

// fetchReleaseList returns the GitHub releases collection, not the
// publication-time based /releases/latest shortcut. It intentionally wraps
// context errors so the application can distinguish a user cancellation from
// the short update-check timeout.
func fetchReleaseList(ctx context.Context, etag string) ([]githubRelease, string, bool, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, githubLatestReleaseURL, nil)
	if err != nil {
		return nil, "", false, fmt.Errorf("无法创建更新请求: %w", err)
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "XIASS-Tools/"+CurrentVersion)
	if strings.TrimSpace(etag) != "" {
		request.Header.Set("If-None-Match", etag)
	}
	response, err := httpClient().Do(request)
	if err != nil {
		return nil, "", false, fmt.Errorf("无法连接 GitHub 更新服务: %w", err)
	}
	defer response.Body.Close()
	responseETag := strings.TrimSpace(response.Header.Get("ETag"))
	if response.StatusCode == http.StatusNotModified {
		return nil, responseETag, true, nil
	}
	if response.StatusCode != http.StatusOK {
		return nil, "", false, fmt.Errorf("检查更新失败（HTTP %d）", response.StatusCode)
	}
	var releases []githubRelease
	if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&releases); err != nil {
		return nil, "", false, fmt.Errorf("更新信息解析失败: %w", err)
	}
	if len(releases) == 0 {
		return nil, "", false, fmt.Errorf("没有可用的稳定版更新")
	}
	return releases, responseETag, false, nil
}

func loadReleaseCache(path string) (releaseCache, bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return releaseCache{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 || len(data) > 2<<20 {
		return releaseCache{}, false
	}
	var cache releaseCache
	if json.Unmarshal(data, &cache) != nil {
		return releaseCache{}, false
	}
	if len(cache.Releases) > 0 {
		return cache, true
	}
	if _, ok := stableReleaseVersion(cache.Release); !ok {
		return releaseCache{}, false
	}
	return cache, true
}

func releaseListFromCache(cache releaseCache) ([]githubRelease, bool) {
	if len(cache.Releases) > 0 {
		return cache.Releases, true
	}
	if _, ok := stableReleaseVersion(cache.Release); ok {
		return []githubRelease{cache.Release}, true
	}
	return nil, false
}

func isLegacyReleaseCache(cache releaseCache) bool {
	return len(cache.Releases) == 0
}

// stableReleaseVersion is intentionally stricter than normalizeVersion. The
// latter remains permissive for comparing the installed version and a skipped
// version, whereas release selection must never accidentally promote a draft,
// prerelease, or a tag such as v1.4.17-rc.1 as a stable installer.
func stableReleaseVersion(release githubRelease) (string, bool) {
	if release.Draft || release.Prerelease {
		return "", false
	}
	raw := strings.TrimSpace(strings.TrimPrefix(strings.ToLower(release.TagName), "v"))
	if raw == "" || strings.ContainsAny(raw, "-+") {
		return "", false
	}
	version := normalizeVersion(raw)
	parts := strings.Split(version, ".")
	if version == "" || len(parts) != 3 {
		return "", false
	}
	return version, true
}

// selectHighestStableRelease applies all release validity rules in one place
// so checking and downloading can never disagree. GitHub returns releases by
// publication time, which is deliberately ignored here; semantic version is
// the primary ordering, with publication time used only as a deterministic
// tiebreaker for malformed duplicate tags.
func selectHighestStableRelease(releases []githubRelease, platform string) (githubRelease, error) {
	var selected githubRelease
	selectedVersion := ""
	for _, release := range releases {
		version, ok := stableReleaseVersion(release)
		if !ok {
			continue
		}
		if _, err := selectInstaller(release, platform); err != nil {
			// A release without this platform's installer cannot be offered as
			// an update even when its version number is the largest one.
			continue
		}
		comparison := compareVersions(version, selectedVersion)
		if selectedVersion == "" || comparison > 0 || (comparison == 0 && release.PublishedAt > selected.PublishedAt) {
			selected = release
			selectedVersion = version
		}
	}
	if selectedVersion == "" {
		return githubRelease{}, fmt.Errorf("没有适用于当前系统的稳定版更新安装包")
	}
	return selected, nil
}

func saveReleaseCache(path string, cache releaseCache) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	data, err := json.Marshal(cache)
	if err != nil {
		return err
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(directory, ".update-release-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	cleanup := func() {
		_ = temp.Close()
		_ = os.Remove(tempPath)
	}
	if err := temp.Chmod(0o600); err != nil {
		cleanup()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		cleanup()
		return err
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		// Windows may reject Rename when the destination already exists. The
		// cache is only a convenience layer, so retrying after removal is safer
		// than leaving every later check unable to refresh its ETag.
		info, statErr := os.Stat(path)
		if statErr != nil || info.IsDir() {
			_ = os.Remove(tempPath)
			return err
		}
		if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
			_ = os.Remove(tempPath)
			return err
		}
		if retryErr := os.Rename(tempPath, path); retryErr != nil {
			_ = os.Remove(tempPath)
			return retryErr
		}
	}
	return nil
}

func releaseChecksum(ctx context.Context, release githubRelease, assetName string) (string, error) {
	var manifest *githubAsset
	for i := range release.Assets {
		if strings.EqualFold(release.Assets[i].Name, "SHA256SUMS.txt") {
			manifest = &release.Assets[i]
			break
		}
	}
	if manifest == nil {
		return "", fmt.Errorf("该版本未提供 SHA256SUMS.txt，已拒绝自动安装")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, manifest.BrowserDownloadURL, nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("User-Agent", "XIASS-Tools/"+CurrentVersion)
	response, err := httpClient().Do(request)
	if err != nil {
		return "", fmt.Errorf("无法下载更新校验清单")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("无法下载更新校验清单（HTTP %d）", response.StatusCode)
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("读取更新校验清单失败")
	}
	for _, line := range strings.Split(string(content), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := strings.TrimPrefix(fields[len(fields)-1], "*")
		if name != assetName {
			continue
		}
		if len(fields[0]) == 64 {
			if _, err := hex.DecodeString(fields[0]); err == nil {
				return fields[0], nil
			}
		}
	}
	return "", fmt.Errorf("校验清单中缺少 %s", assetName)
}

func selectInstaller(release githubRelease, platform string) (githubAsset, error) {
	version, ok := stableReleaseVersion(release)
	if !ok {
		return githubAsset{}, fmt.Errorf("该版本没有有效的稳定版标签")
	}
	// The release workflow deliberately publishes exactly one installer for each
	// platform. Do not use a fuzzy extension/name match here: an accidentally
	// attached portable build or unrelated package must never become an
	// in-app-update candidate merely because it contains "windows" or "macos".
	// Keeping this contract identical to release.yml also makes the SHA256
	// manifest unambiguous.
	expectedName := ""
	switch platform {
	case "darwin":
		expectedName = fmt.Sprintf("XIASS-Tools-macOS-universal-v%s-Installer.pkg", version)
	case "windows":
		expectedName = fmt.Sprintf("XIASS-Tools-Windows-x64-v%s-Setup.exe", version)
	default:
		return githubAsset{}, fmt.Errorf("该版本没有适用于当前系统的安装包")
	}
	assets := append([]githubAsset(nil), release.Assets...)
	sort.Slice(assets, func(i, j int) bool { return assets[i].Name < assets[j].Name })
	for _, asset := range assets {
		if strings.EqualFold(asset.Name, expectedName) {
			return asset, nil
		}
	}
	return githubAsset{}, fmt.Errorf("该版本没有适用于当前系统的安装包")
}

func normalizeVersion(value string) string {
	value = strings.TrimSpace(strings.TrimPrefix(strings.ToLower(value), "v"))
	if value == "" {
		return ""
	}
	for _, piece := range strings.Split(strings.SplitN(value, "-", 2)[0], ".") {
		if piece == "" {
			return ""
		}
		if _, err := strconv.Atoi(piece); err != nil {
			return ""
		}
	}
	return value
}

func compareVersions(left, right string) int {
	left, right = normalizeVersion(left), normalizeVersion(right)
	if left == "" || right == "" {
		return 0
	}
	leftParts, rightParts := strings.Split(left, "."), strings.Split(right, ".")
	length := len(leftParts)
	if len(rightParts) > length {
		length = len(rightParts)
	}
	for i := 0; i < length; i++ {
		var l, r int
		if i < len(leftParts) {
			l, _ = strconv.Atoi(leftParts[i])
		}
		if i < len(rightParts) {
			r, _ = strconv.Atoi(rightParts[i])
		}
		if l > r {
			return 1
		}
		if l < r {
			return -1
		}
	}
	return 0
}

func installerExtension(name string) string {
	extension := strings.TrimPrefix(strings.ToLower(filepath.Ext(name)), ".")
	if extension == "pkg" || extension == "exe" {
		return extension
	}
	return "bin"
}

func truncateNotes(value string) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) > 6000 {
		return string(runes[:6000]) + "…"
	}
	return value
}

func httpClient() *http.Client {
	return &http.Client{Timeout: 45 * time.Second}
}
