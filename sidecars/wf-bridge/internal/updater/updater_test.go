package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func nextPatchTestVersion(t *testing.T) string {
	t.Helper()
	parts := strings.Split(normalizeVersion(CurrentVersion), ".")
	if len(parts) != 3 {
		t.Fatalf("CurrentVersion is not semantic: %q", CurrentVersion)
	}
	patch, err := strconv.Atoi(parts[2])
	if err != nil {
		t.Fatalf("CurrentVersion patch is not numeric: %q", CurrentVersion)
	}
	return fmt.Sprintf("%s.%s.%d", parts[0], parts[1], patch+1)
}

func testAvailableReleaseJSON(t *testing.T) []byte {
	t.Helper()
	version := nextPatchTestVersion(t)
	return marshalReleaseList(t, []githubRelease{testRelease(version, "2026-08-04T00:00:00Z", "darwin", "windows")})
}

func useLatestReleaseServer(t *testing.T, handler http.Handler) {
	t.Helper()
	server := httptest.NewServer(handler)
	previous := githubLatestReleaseURL
	githubLatestReleaseURL = server.URL
	t.Cleanup(func() {
		githubLatestReleaseURL = previous
		server.Close()
	})
}

func testInstallerAsset(version, platform string) githubAsset {
	if platform == "windows" {
		return githubAsset{
			Name: fmt.Sprintf("XIASS-Tools-Windows-x64-v%s-Setup.exe", version),
			Size: 1,
		}
	}
	return githubAsset{
		Name: fmt.Sprintf("XIASS-Tools-macOS-universal-v%s-Installer.pkg", version),
		Size: 1,
	}
}

func testRelease(version, publishedAt string, platforms ...string) githubRelease {
	assets := make([]githubAsset, 0, len(platforms))
	for _, platform := range platforms {
		assets = append(assets, testInstallerAsset(version, platform))
	}
	return githubRelease{
		TagName:     "v" + version,
		HTMLURL:     "https://example.test/releases/v" + version,
		PublishedAt: publishedAt,
		Assets:      assets,
	}
}

func marshalReleaseList(t *testing.T, releases []githubRelease) []byte {
	t.Helper()
	payload, err := json.Marshal(releases)
	if err != nil {
		t.Fatalf("marshal releases: %v", err)
	}
	return payload
}

func TestSelectHighestStableReleaseUsesSemanticVersionAndPlatformAsset(t *testing.T) {
	releases := []githubRelease{
		// This is newer by publication time but intentionally lower by version.
		testRelease("1.4.6", "2026-08-07T00:00:00Z", "darwin", "windows"),
		// Keep these versions distinct.  A regression once used the same tag for
		// both rows, which allowed a Windows-only release to mask a macOS asset
		// without the assertion being able to tell the two releases apart.
		testRelease("1.5.2", "2026-08-01T00:00:00Z", "darwin", "windows"),
		// The highest tag is not an update for macOS because its release lacks
		// the matching installer; it must not hide v1.5.2.
		testRelease("1.5.5", "2026-08-06T00:00:00Z", "windows"),
		{TagName: "v1.4.99", Prerelease: true, Assets: []githubAsset{testInstallerAsset("1.4.99", "darwin")}},
		{TagName: "v1.4.98-rc.1", Assets: []githubAsset{testInstallerAsset("1.4.98", "darwin")}},
	}

	macRelease, err := selectHighestStableRelease(releases, "darwin")
	if err != nil {
		t.Fatalf("select macOS release: %v", err)
	}
	if got := normalizeVersion(macRelease.TagName); got != "1.5.2" {
		t.Fatalf("macOS selected %s, want 1.5.2", got)
	}

	windowsRelease, err := selectHighestStableRelease(releases, "windows")
	if err != nil {
		t.Fatalf("select Windows release: %v", err)
	}
	if got := normalizeVersion(windowsRelease.TagName); got != "1.5.5" {
		t.Fatalf("Windows selected %s, want 1.5.5", got)
	}
}

func TestSelectInstallerRequiresCanonicalReleaseAsset(t *testing.T) {
	release := githubRelease{
		TagName: "v1.5.5",
		Assets: []githubAsset{
			// These are deliberately plausible names, but neither is an asset
			// produced by release.yml. A future accidental upload must not make
			// the in-app updater install it.
			{Name: "XIASS-Tools-macOS-v1.5.5-Installer.pkg"},
			{Name: "XIASS-Tools-Windows-x64-v1.5.5-Portable.exe"},
			{Name: "unrelated-windows-setup.exe"},
		},
	}
	for _, platform := range []string{"darwin", "windows"} {
		if _, err := selectInstaller(release, platform); err == nil {
			t.Fatalf("%s accepted a non-canonical release asset", platform)
		}
	}

	canonical := testInstallerAsset("1.5.5", "darwin")
	release.Assets = append(release.Assets, canonical)
	asset, err := selectInstaller(release, "darwin")
	if err != nil || asset.Name != canonical.Name {
		t.Fatalf("canonical macOS installer selection = %#v, %v", asset, err)
	}
}

func TestCheckWithCacheUsesHighestSemanticVersionRatherThanPublicationTime(t *testing.T) {
	nextVersion := nextPatchTestVersion(t)
	releases := []githubRelease{
		testRelease("1.4.6", "2026-08-07T00:00:00Z", "darwin", "windows"),
		testRelease(nextVersion, "2026-08-01T00:00:00Z", "darwin", "windows"),
	}
	useLatestReleaseServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write(marshalReleaseList(t, releases))
	}))

	info, err := CheckWithCache(context.Background(), "", filepath.Join(t.TempDir(), "release-cache.json"))
	if err != nil {
		t.Fatalf("check update: %v", err)
	}
	if info.LatestVersion != nextVersion || !info.Available {
		t.Fatalf("check info = %#v, want available v%s", info, nextVersion)
	}
}

func TestCheckDoesNotOfferLowerVersionAsAnUpdate(t *testing.T) {
	releases := []githubRelease{testRelease("1.4.5", "2026-08-07T00:00:00Z", "darwin", "windows")}
	useLatestReleaseServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write(marshalReleaseList(t, releases))
	}))

	info, err := CheckWithCache(context.Background(), "", filepath.Join(t.TempDir(), "release-cache.json"))
	if err != nil {
		t.Fatalf("check lower release: %v", err)
	}
	if info.Available || info.LatestVersion != "1.4.5" {
		t.Fatalf("lower release was treated as an update: %#v", info)
	}
}

func TestCheckWithCacheMigratesLegacySingleReleaseCache(t *testing.T) {
	nextVersion := nextPatchTestVersion(t)
	cachePath := filepath.Join(t.TempDir(), "release-cache.json")
	legacy := releaseCache{
		ETag:      `"legacy-latest-endpoint"`,
		CheckedAt: time.Now().UTC().Format(time.RFC3339),
		Release:   testRelease("1.4.6", "2026-08-07T00:00:00Z", "darwin", "windows"),
	}
	if err := saveReleaseCache(cachePath, legacy); err != nil {
		t.Fatalf("save legacy cache: %v", err)
	}
	releases := []githubRelease{
		legacy.Release,
		testRelease(nextVersion, "2026-08-01T00:00:00Z", "darwin", "windows"),
	}
	requests := 0
	useLatestReleaseServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		if got := request.Header.Get("If-None-Match"); got != "" {
			t.Fatalf("legacy /releases/latest ETag was reused for /releases: %q", got)
		}
		writer.Header().Set("ETag", `"release-list"`)
		_, _ = writer.Write(marshalReleaseList(t, releases))
	}))

	info, err := CheckWithCache(context.Background(), "", cachePath)
	if err != nil {
		t.Fatalf("migrate legacy cache: %v", err)
	}
	if requests != 1 || info.LatestVersion != nextVersion || !info.Available {
		t.Fatalf("legacy migration result = requests:%d info:%#v", requests, info)
	}
	migrated, ok := loadReleaseCache(cachePath)
	if !ok || migrated.Format != releaseCacheFormat || len(migrated.Releases) != len(releases) || isLegacyReleaseCache(migrated) {
		t.Fatalf("legacy cache did not migrate: %#v", migrated)
	}
}

func TestDownloadLatestInstallerUsesSameHighestSemanticRelease(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		t.Skipf("installer selection is only defined for desktop targets, got %s", runtime.GOOS)
	}
	nextVersion := nextPatchTestVersion(t)
	installer := []byte("verified v" + nextVersion + " installer")
	sum := sha256.Sum256(installer)
	checksum := hex.EncodeToString(sum[:])
	platform := runtime.GOOS
	oldInstaller := testInstallerAsset("1.4.6", platform)
	newInstaller := testInstallerAsset(nextVersion, platform)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/":
			oldInstaller.BrowserDownloadURL = server.URL + "/v146.installer"
			newInstaller.BrowserDownloadURL = server.URL + "/next.installer"
			oldInstaller.Size = 1
			newInstaller.Size = int64(len(installer))
			releases := []githubRelease{
				{
					TagName:     "v1.4.6",
					PublishedAt: "2026-08-07T00:00:00Z",
					Assets: []githubAsset{
						oldInstaller,
						{Name: "SHA256SUMS.txt", BrowserDownloadURL: server.URL + "/v146.sha", Size: 1},
					},
				},
				{
					TagName:     "v" + nextVersion,
					PublishedAt: "2026-08-01T00:00:00Z",
					Assets: []githubAsset{
						newInstaller,
						{Name: "SHA256SUMS.txt", BrowserDownloadURL: server.URL + "/next.sha", Size: 1},
					},
				},
			}
			_, _ = writer.Write(marshalReleaseList(t, releases))
		case "/next.sha":
			_, _ = fmt.Fprintf(writer, "%s  %s\n", checksum, newInstaller.Name)
		case "/next.installer":
			_, _ = writer.Write(installer)
		case "/v146.installer", "/v146.sha":
			t.Fatalf("download attempted lower publication-time release %s", request.URL.Path)
		default:
			http.NotFound(writer, request)
		}
	}))
	previous := githubLatestReleaseURL
	githubLatestReleaseURL = server.URL
	t.Cleanup(func() {
		githubLatestReleaseURL = previous
		server.Close()
	})

	path, info, err := DownloadLatestInstaller(context.Background(), nil)
	if err != nil {
		t.Fatalf("download highest release: %v", err)
	}
	defer os.Remove(path)
	if info.LatestVersion != nextVersion || info.AssetName != newInstaller.Name {
		t.Fatalf("download info = %#v", info)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != string(installer) {
		t.Fatalf("verified installer contents = %q, err = %v", data, err)
	}
}

func TestCheckWithCacheUsesETagAnd304(t *testing.T) {
	nextVersion := nextPatchTestVersion(t)
	requests := 0
	useLatestReleaseServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		if requests == 1 {
			if got := request.Header.Get("If-None-Match"); got != "" {
				t.Fatalf("first request ETag = %q, want empty", got)
			}
			writer.Header().Set("ETag", `"release-1"`)
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write(testAvailableReleaseJSON(t))
			return
		}
		if got := request.Header.Get("If-None-Match"); got != `"release-1"` {
			t.Fatalf("conditional request ETag = %q", got)
		}
		writer.Header().Set("ETag", `"release-1"`)
		writer.WriteHeader(http.StatusNotModified)
	}))

	cachePath := filepath.Join(t.TempDir(), "release-cache.json")
	first, err := CheckWithCache(context.Background(), "", cachePath)
	if err != nil {
		t.Fatalf("first check: %v", err)
	}
	if !first.Available || first.Cached || first.LatestVersion != nextVersion || first.CheckedAt == "" {
		t.Fatalf("first check info = %#v", first)
	}
	second, err := CheckWithCache(context.Background(), "", cachePath)
	if err != nil {
		t.Fatalf("fresh cache check: %v", err)
	}
	if !second.Available || !second.Cached || second.CacheReason != "fresh" || second.LatestVersion != nextVersion || second.CheckedAt == "" {
		t.Fatalf("fresh cache info = %#v", second)
	}
	if requests != 1 {
		t.Fatalf("fresh cache requests = %d, want 1", requests)
	}

	makeReleaseCacheStale(t, cachePath)
	third, err := CheckWithCache(context.Background(), "", cachePath)
	if err != nil {
		t.Fatalf("conditional check: %v", err)
	}
	if !third.Available || third.Cached || third.LatestVersion != nextVersion || third.CheckedAt == "" {
		t.Fatalf("conditional check info = %#v", third)
	}
	if requests != 2 {
		t.Fatalf("stale cache requests = %d, want 2", requests)
	}
}

func makeReleaseCacheStale(t *testing.T, cachePath string) {
	t.Helper()
	cache, ok := loadReleaseCache(cachePath)
	if !ok {
		t.Fatal("release cache was not saved")
	}
	cache.CheckedAt = time.Now().Add(-FreshCacheTTL - time.Minute).UTC().Format(time.RFC3339)
	if err := saveReleaseCache(cachePath, cache); err != nil {
		t.Fatalf("age release cache: %v", err)
	}
}

func TestCheckWithCacheFallsBackAfterServerFailure(t *testing.T) {
	nextVersion := nextPatchTestVersion(t)
	requests := 0
	useLatestReleaseServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		if requests == 1 {
			writer.Header().Set("ETag", `"release-1"`)
			_, _ = writer.Write(testAvailableReleaseJSON(t))
			return
		}
		http.Error(writer, "temporarily unavailable", http.StatusServiceUnavailable)
	}))

	cachePath := filepath.Join(t.TempDir(), "release-cache.json")
	if _, err := CheckWithCache(context.Background(), "", cachePath); err != nil {
		t.Fatalf("seed cache: %v", err)
	}
	makeReleaseCacheStale(t, cachePath)
	info, err := CheckWithCache(context.Background(), "", cachePath)
	if err != nil {
		t.Fatalf("fallback check: %v", err)
	}
	if !info.Cached || info.CacheReason != "network" || !info.Available || info.LatestVersion != nextVersion {
		t.Fatalf("fallback info = %#v", info)
	}
}

func TestCheckWithCacheMarksTimeoutFallback(t *testing.T) {
	nextVersion := nextPatchTestVersion(t)
	requests := 0
	useLatestReleaseServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		if requests == 1 {
			writer.Header().Set("ETag", `"release-1"`)
			_, _ = writer.Write(testAvailableReleaseJSON(t))
			return
		}
		<-request.Context().Done()
	}))

	cachePath := filepath.Join(t.TempDir(), "release-cache.json")
	if _, err := CheckWithCache(context.Background(), "", cachePath); err != nil {
		t.Fatalf("seed cache: %v", err)
	}
	makeReleaseCacheStale(t, cachePath)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	info, err := CheckWithCache(ctx, "", cachePath)
	if err != nil {
		t.Fatalf("timeout fallback: %v", err)
	}
	if !info.Cached || info.CacheReason != "timeout" || info.LatestVersion != nextVersion {
		t.Fatalf("timeout fallback info = %#v", info)
	}
}

func TestCheckWithCacheHonorsCancellationWithoutUsingCache(t *testing.T) {
	started := make(chan struct{})
	requests := 0
	useLatestReleaseServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		if requests == 1 {
			writer.Header().Set("ETag", `"release-1"`)
			_, _ = writer.Write(testAvailableReleaseJSON(t))
			return
		}
		close(started)
		<-request.Context().Done()
	}))
	cachePath := filepath.Join(t.TempDir(), "release-cache.json")
	if _, err := CheckWithCache(context.Background(), "", cachePath); err != nil {
		t.Fatalf("seed cache: %v", err)
	}

	// Explicit cancellation must win even if the cache is still fresh.
	freshCtx, freshCancel := context.WithCancel(context.Background())
	freshCancel()
	if _, err := CheckWithCache(freshCtx, "", cachePath); !errors.Is(err, context.Canceled) {
		t.Fatalf("fresh-cache cancellation error = %v", err)
	}
	if requests != 1 {
		t.Fatalf("cancelled fresh cache made %d requests, want 1", requests)
	}

	makeReleaseCacheStale(t, cachePath)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := CheckWithCache(ctx, "", cachePath)
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("update request did not start")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled update check did not return promptly")
	}
}

func TestCheckTimeoutStaysShort(t *testing.T) {
	if CheckTimeout != 5*time.Second {
		t.Fatalf("CheckTimeout = %s, want 5s", CheckTimeout)
	}
	if FreshCacheTTL != 10*time.Minute {
		t.Fatalf("FreshCacheTTL = %s, want 10m", FreshCacheTTL)
	}
}
