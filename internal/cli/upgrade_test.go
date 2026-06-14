package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/baseloop-hq/baseloop-cli/internal/version"
)

func setVersion(t *testing.T, v string) {
	t.Helper()
	old := version.Version
	t.Cleanup(func() { version.Version = old })
	version.Version = v
}

func setUpgradeTarget(t *testing.T, path string) {
	t.Helper()
	old := upgradeTargetPath
	t.Cleanup(func() { upgradeTargetPath = old })
	upgradeTargetPath = func() (string, error) { return path, nil }
}

// stubTransport replaces the default transport with handler and returns a
// counter of requests made through it.
func stubTransport(t *testing.T, handler func(url string) *http.Response) *int {
	t.Helper()
	old := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = old })
	calls := new(int)
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		*calls++
		return handler(r.URL.String()), nil
	})
	return calls
}

func httpBody(status int, body []byte) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(body)), Header: http.Header{}}
}

func platformAssetName(tag string) string {
	return fmt.Sprintf("baseloop_%s_%s_%s.tar.gz", strings.TrimPrefix(tag, "v"), runtime.GOOS, runtime.GOARCH)
}

// cliReleaseJSON builds a one-release GitHub releases payload carrying this
// platform's archive asset and, optionally, a checksums.txt asset.
func cliReleaseJSON(t *testing.T, tag string, withChecksums bool) []byte {
	t.Helper()
	assets := []map[string]string{
		{"name": platformAssetName(tag), "browser_download_url": "https://rel.test/assets/" + platformAssetName(tag)},
	}
	if withChecksums {
		assets = append(assets, map[string]string{"name": "checksums.txt", "browser_download_url": "https://rel.test/assets/checksums.txt"})
	}
	payload, err := json.Marshal([]map[string]any{{"tag_name": tag, "prerelease": false, "assets": assets}})
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

// stubUpgradeRelease serves a complete release for tag: the releases listing,
// the platform archive containing binaryContent, and a checksums.txt entry
// hashing checksumOf (pass the archive bytes for a valid checksum, anything
// else to force a mismatch).
func stubUpgradeRelease(t *testing.T, tag string, archive []byte, checksumOf []byte) *int {
	t.Helper()
	sum := sha256.Sum256(checksumOf)
	checksums := []byte(hex.EncodeToString(sum[:]) + "  " + platformAssetName(tag) + "\n")
	releases := cliReleaseJSON(t, tag, true)
	return stubTransport(t, func(url string) *http.Response {
		switch {
		case strings.Contains(url, "/releases"):
			return httpBody(200, releases)
		case strings.Contains(url, "checksums.txt"):
			return httpBody(200, checksums)
		case strings.Contains(url, platformAssetName(tag)):
			return httpBody(200, archive)
		default:
			return httpBody(500, []byte("unexpected: "+url))
		}
	})
}

func TestVersionOutdated(t *testing.T) {
	cases := []struct {
		current, latest string
		want            bool
	}{
		{"0.1.0", "v0.2.0", true},
		{"0.2.0", "v0.2.0", false},
		{"0.3.0", "v0.2.0", false},
		{"0.9.9", "v0.10.0", true},
		{"1.2", "v1.2.1", true},
		{"0.2.0-rc1", "v0.2.0", false},
		{"dev", "v0.2.0", false},
		{"0.1.0", "", false},
		{"0.1.0", "nightly", false},
	}
	for _, c := range cases {
		if got := versionOutdated(c.current, c.latest); got != c.want {
			t.Errorf("versionOutdated(%q, %q) = %v, want %v", c.current, c.latest, got, c.want)
		}
	}
}

func TestSelectCLIAssetSkipsPrereleasesAndMatchesPlatform(t *testing.T) {
	releases := []githubRelease{
		{TagName: "v0.3.0-rc1", Prerelease: true, Assets: []githubReleaseAsset{
			{Name: "baseloop_0.3.0-rc1_linux_amd64.tar.gz", BrowserDownloadURL: "https://rel.test/rc"},
		}},
		{TagName: "v0.2.0", Assets: []githubReleaseAsset{
			{Name: "baseloop_0.2.0_linux_amd64.tar.gz", BrowserDownloadURL: "https://rel.test/linux"},
			{Name: "baseloop_0.2.0_windows_amd64.zip", BrowserDownloadURL: "https://rel.test/windows"},
			{Name: "checksums.txt", BrowserDownloadURL: "https://rel.test/sums"},
		}},
	}
	tag, assetURL, checksumsURL := selectCLIAsset(releases, "linux", "amd64")
	if tag != "v0.2.0" || assetURL != "https://rel.test/linux" || checksumsURL != "https://rel.test/sums" {
		t.Fatalf("got (%q, %q, %q)", tag, assetURL, checksumsURL)
	}
	tag, assetURL, checksumsURL = selectCLIAsset(releases, "windows", "amd64")
	if tag != "v0.2.0" || assetURL != "https://rel.test/windows" || checksumsURL != "https://rel.test/sums" {
		t.Fatalf("got (%q, %q, %q)", tag, assetURL, checksumsURL)
	}
	if _, assetURL, _ = selectCLIAsset(releases, "plan9", "mips"); assetURL != "" {
		t.Fatalf("expected no asset for unknown platform, got %q", assetURL)
	}
	// An arm64 asset must not satisfy an arm lookup (suffix, not substring).
	if _, assetURL, _ = selectCLIAsset([]githubRelease{{TagName: "v0.2.0", Assets: []githubReleaseAsset{
		{Name: "baseloop_0.2.0_linux_arm64.tar.gz", BrowserDownloadURL: "https://rel.test/arm64"},
	}}}, "linux", "arm"); assetURL != "" {
		t.Fatalf("expected no asset for linux/arm, got %q", assetURL)
	}
}

func TestUpgradeRefusesDevBuild(t *testing.T) {
	setVersion(t, "dev")
	var out bytes.Buffer
	if code := Run([]string{"upgrade", "--json"}, &out, &out); code != 1 {
		t.Fatalf("expected exit 1, got %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "development build") {
		t.Fatalf("expected dev-build refusal, got %s", out.String())
	}
}

func TestUpgradeAlreadyCurrent(t *testing.T) {
	t.Setenv("BASELOOP_STATE", t.TempDir())
	t.Setenv("BASELOOP_RELEASES_API_URL", "https://rel.test/releases")
	setVersion(t, "0.2.0")
	stubTransport(t, func(url string) *http.Response {
		return httpBody(200, cliReleaseJSON(t, "v0.2.0", true))
	})

	var out bytes.Buffer
	if code := Run([]string{"upgrade", "--json"}, &out, &out); code != 0 {
		t.Fatalf("expected exit 0, got %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "Already up to date.") {
		t.Fatalf("expected up-to-date message, got %s", out.String())
	}
}

func TestUpgradeSwapsBinaryAndRecordsCheck(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test fixture builds a unix archive")
	}
	stateDir := t.TempDir()
	t.Setenv("BASELOOP_STATE", stateDir)
	t.Setenv("BASELOOP_RELEASES_API_URL", "https://rel.test/releases")
	t.Setenv("BASELOOP_SKIP_SETUP", "1")
	setVersion(t, "0.1.0")

	target := filepath.Join(t.TempDir(), "baseloop")
	if err := os.WriteFile(target, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	setUpgradeTarget(t, target)

	archive := writeTarGzBytes(t, map[string]string{"baseloop": "new binary 0.2.0"})
	stubUpgradeRelease(t, "v0.2.0", archive, archive)

	var out bytes.Buffer
	if code := Run([]string{"upgrade", "--json"}, &out, &out); code != 0 {
		t.Fatalf("expected exit 0, got %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "Upgraded baseloop to v0.2.0.") {
		t.Fatalf("expected upgrade message, got %s", out.String())
	}
	swapped, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(swapped) != "new binary 0.2.0" {
		t.Fatalf("expected swapped binary, got %q", swapped)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("expected executable binary, got mode %v", info.Mode())
	}
	if _, err := os.Stat(target + ".old"); !os.IsNotExist(err) {
		t.Fatalf("expected .old backup removed, got %v", err)
	}
	cache, err := os.ReadFile(filepath.Join(stateDir, versionCheckFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cache), "v0.2.0") {
		t.Fatalf("expected version check cache with v0.2.0, got %s", cache)
	}
	if !strings.Contains(out.String(), "BASELOOP_SKIP_SETUP") {
		t.Fatalf("expected skipped-setup note, got %s", out.String())
	}
}

func TestUpgradeChecksumMismatchKeepsBinary(t *testing.T) {
	t.Setenv("BASELOOP_STATE", t.TempDir())
	t.Setenv("BASELOOP_RELEASES_API_URL", "https://rel.test/releases")
	t.Setenv("BASELOOP_SKIP_SETUP", "1")
	setVersion(t, "0.1.0")

	target := filepath.Join(t.TempDir(), "baseloop")
	if err := os.WriteFile(target, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	setUpgradeTarget(t, target)

	archive := writeTarGzBytes(t, map[string]string{"baseloop": "tampered"})
	stubUpgradeRelease(t, "v0.2.0", archive, []byte("something else entirely"))

	var out bytes.Buffer
	if code := Run([]string{"upgrade", "--json"}, &out, &out); code != 1 {
		t.Fatalf("expected exit 1, got %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "checksum mismatch") {
		t.Fatalf("expected checksum mismatch error, got %s", out.String())
	}
	kept, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(kept) != "old binary" {
		t.Fatalf("expected binary untouched after failed verification, got %q", kept)
	}
}

func TestUpgradeRefusesUnverifiedRelease(t *testing.T) {
	t.Setenv("BASELOOP_STATE", t.TempDir())
	t.Setenv("BASELOOP_RELEASES_API_URL", "https://rel.test/releases")
	setVersion(t, "0.1.0")
	setUpgradeTarget(t, filepath.Join(t.TempDir(), "baseloop"))
	stubTransport(t, func(url string) *http.Response {
		return httpBody(200, cliReleaseJSON(t, "v0.2.0", false))
	})

	var out bytes.Buffer
	if code := Run([]string{"upgrade", "--json"}, &out, &out); code != 1 {
		t.Fatalf("expected exit 1, got %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "refusing unverified") {
		t.Fatalf("expected unverified refusal, got %s", out.String())
	}
}

func TestCLIVersionAdvisoryOutdated(t *testing.T) {
	t.Setenv("BASELOOP_STATE", t.TempDir())
	t.Setenv("BASELOOP_RELEASES_API_URL", "https://rel.test/releases")
	setVersion(t, "0.1.0")
	stubTransport(t, func(url string) *http.Response {
		return httpBody(200, cliReleaseJSON(t, "v0.2.0", true))
	})

	ok, hint, show := cliVersionAdvisory()
	if !show || ok {
		t.Fatalf("expected outdated advisory, got ok=%v show=%v", ok, show)
	}
	if !strings.Contains(hint, "baseloop upgrade") || !strings.Contains(hint, "v0.2.0") {
		t.Fatalf("expected upgrade hint with latest version, got %q", hint)
	}
}

func TestCLIVersionAdvisoryUsesFreshCache(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("BASELOOP_STATE", stateDir)
	setVersion(t, "0.1.0")
	cache, err := json.Marshal(versionCheck{CheckedAt: time.Now().UTC(), Latest: "v0.9.0"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, versionCheckFile), cache, 0o600); err != nil {
		t.Fatal(err)
	}
	calls := stubTransport(t, func(url string) *http.Response {
		return httpBody(500, []byte("down"))
	})

	ok, hint, show := cliVersionAdvisory()
	if *calls != 0 {
		t.Fatalf("expected no network lookup with a fresh cache, got %d calls", *calls)
	}
	if !show || ok || !strings.Contains(hint, "v0.9.0") {
		t.Fatalf("expected cached outdated advisory, got ok=%v show=%v hint=%q", ok, show, hint)
	}
}

func TestCLIVersionAdvisoryCachesLookupFailure(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("BASELOOP_STATE", stateDir)
	t.Setenv("BASELOOP_RELEASES_API_URL", "https://rel.test/releases")
	setVersion(t, "0.1.0")
	calls := stubTransport(t, func(url string) *http.Response {
		return httpBody(500, []byte("down"))
	})

	if _, _, show := cliVersionAdvisory(); show {
		t.Fatal("expected no advisory when the lookup fails")
	}
	if *calls != 1 {
		t.Fatalf("expected one lookup, got %d", *calls)
	}
	if _, _, show := cliVersionAdvisory(); show {
		t.Fatal("expected no advisory on cached failure")
	}
	if *calls != 1 {
		t.Fatalf("expected failure to be cached for the TTL, got %d calls", *calls)
	}
}

func TestCLIVersionAdvisorySkipsDevAndOptOut(t *testing.T) {
	t.Setenv("BASELOOP_STATE", t.TempDir())
	calls := stubTransport(t, func(url string) *http.Response {
		return httpBody(200, cliReleaseJSON(t, "v0.2.0", true))
	})

	setVersion(t, "dev")
	if _, _, show := cliVersionAdvisory(); show {
		t.Fatal("expected no advisory for dev builds")
	}

	setVersion(t, "0.1.0")
	t.Setenv("BASELOOP_NO_UPDATE_CHECK", "1")
	if _, _, show := cliVersionAdvisory(); show {
		t.Fatal("expected no advisory with BASELOOP_NO_UPDATE_CHECK")
	}
	if *calls != 0 {
		t.Fatalf("expected no network lookups, got %d", *calls)
	}
}

func writeVersionCheckCache(t *testing.T, stateDir, latest string) {
	t.Helper()
	cache, err := json.Marshal(versionCheck{CheckedAt: time.Now().UTC(), Latest: latest})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, versionCheckFile), cache, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateNoticeOnOrdinaryCommand(t *testing.T) {
	t.Setenv("BASELOOP_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	stateDir := t.TempDir()
	t.Setenv("BASELOOP_STATE", stateDir)
	setVersion(t, "0.1.0")
	writeVersionCheckCache(t, stateDir, "v0.2.0")
	calls := stubTransport(t, func(url string) *http.Response {
		return httpBody(500, []byte("down"))
	})

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"auth", "status"}, &stdout, &stderr); code != 0 {
		t.Fatalf("expected exit 0, got %d: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "Run: baseloop upgrade") || !strings.Contains(stderr.String(), "v0.2.0") {
		t.Fatalf("expected update notice on stderr, got %q", stderr.String())
	}
	if strings.Contains(stdout.String(), "Run: baseloop upgrade") {
		t.Fatalf("expected notice on stderr only, stdout got %q", stdout.String())
	}
	if *calls != 0 {
		t.Fatalf("expected fresh cache to avoid network, got %d calls", *calls)
	}
}

func TestUpdateNoticeRefreshesStaleCache(t *testing.T) {
	t.Setenv("BASELOOP_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	stateDir := t.TempDir()
	t.Setenv("BASELOOP_STATE", stateDir)
	t.Setenv("BASELOOP_RELEASES_API_URL", "https://rel.test/releases")
	setVersion(t, "0.1.0")
	calls := stubTransport(t, func(url string) *http.Response {
		return httpBody(200, cliReleaseJSON(t, "v0.2.0", true))
	})

	var stdout, stderr bytes.Buffer
	Run([]string{"auth", "status"}, &stdout, &stderr)
	if *calls != 1 {
		t.Fatalf("expected one release lookup, got %d", *calls)
	}
	if !strings.Contains(stderr.String(), "Run: baseloop upgrade") {
		t.Fatalf("expected update notice after refresh, got %q", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(stateDir, versionCheckFile)); err != nil {
		t.Fatalf("expected version check cache written: %v", err)
	}
}

func TestUpdateNoticeSilences(t *testing.T) {
	t.Setenv("BASELOOP_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	stateDir := t.TempDir()
	t.Setenv("BASELOOP_STATE", stateDir)
	writeVersionCheckCache(t, stateDir, "v0.2.0")
	stubTransport(t, func(url string) *http.Response {
		return httpBody(500, []byte("down"))
	})

	// Current binary: no notice.
	setVersion(t, "0.2.0")
	var stdout, stderr bytes.Buffer
	Run([]string{"auth", "status"}, &stdout, &stderr)
	if strings.Contains(stderr.String(), "baseloop upgrade") {
		t.Fatalf("expected no notice when current, got %q", stderr.String())
	}

	// Outdated but excluded command: no notice.
	setVersion(t, "0.1.0")
	stderr.Reset()
	Run([]string{"version"}, &stdout, &stderr)
	if strings.Contains(stderr.String(), "baseloop upgrade") {
		t.Fatalf("expected no notice on version command, got %q", stderr.String())
	}

	// Outdated but opted out: no notice.
	t.Setenv("BASELOOP_NO_UPDATE_CHECK", "1")
	stderr.Reset()
	Run([]string{"auth", "status"}, &stdout, &stderr)
	if strings.Contains(stderr.String(), "baseloop upgrade") {
		t.Fatalf("expected no notice with BASELOOP_NO_UPDATE_CHECK, got %q", stderr.String())
	}
}

func TestDoctorEmitsCLIVersionAdvisory(t *testing.T) {
	t.Setenv("BASELOOP_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("BASELOOP_STATE", t.TempDir())
	t.Setenv("HERMES_HOME", "")
	t.Setenv("BASELOOP_TOKEN", "")
	setVersion(t, "0.1.0")
	stubTransport(t, func(url string) *http.Response {
		if strings.Contains(url, "/releases") {
			return httpBody(200, cliReleaseJSON(t, "v0.2.0", true))
		}
		return httpBody(500, []byte("down"))
	})

	var out bytes.Buffer
	Run([]string{"doctor", "--json"}, &out, &out)
	if !strings.Contains(out.String(), "cli_version") {
		t.Fatalf("expected cli_version advisory, got %s", out.String())
	}
	if !strings.Contains(out.String(), "Run baseloop upgrade to get v0.2.0") {
		t.Fatalf("expected upgrade hint, got %s", out.String())
	}
}

func TestBackgroundUpgradeHappyPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test fixture builds a unix archive")
	}
	stateDir := t.TempDir()
	t.Setenv("BASELOOP_STATE", stateDir)
	t.Setenv("BASELOOP_RELEASES_API_URL", "https://rel.test/releases")
	t.Setenv("BASELOOP_SKIP_SETUP", "1")
	setVersion(t, "0.1.0")

	target := filepath.Join(t.TempDir(), "baseloop")
	if err := os.WriteFile(target, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	setUpgradeTarget(t, target)

	// Pre-existing failure record and stale log content: success must clear
	// the record, and the lock winner must truncate the log.
	recordAutoUpdateFailure("v0.2.0", "earlier failure", false)
	logPath := filepath.Join(stateDir, autoUpdateLogFile)
	if err := os.WriteFile(logPath, []byte("stale loser output\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	archive := writeTarGzBytes(t, map[string]string{"baseloop": "new binary 0.2.0"})
	stubUpgradeRelease(t, "v0.2.0", archive, archive)

	var out bytes.Buffer
	if code := Run([]string{"upgrade", "--background", "--json"}, &out, &out); code != 0 {
		t.Fatalf("expected exit 0, got %d: %s", code, out.String())
	}
	swapped, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(swapped) != "new binary 0.2.0" {
		t.Fatalf("expected swapped binary, got %q", swapped)
	}
	if _, ok := readAutoUpdateFailure(); ok {
		t.Fatal("expected failure record cleared on success")
	}
	if _, err := os.Stat(filepath.Join(stateDir, upgradeLockFile)); !os.IsNotExist(err) {
		t.Fatalf("expected lock released, got %v", err)
	}
	logContent, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(logContent), "stale loser output") {
		t.Fatalf("expected lock winner to truncate the log, got %q", logContent)
	}
}

func TestBackgroundUpgradeLockHeldExitsQuiet(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("BASELOOP_STATE", stateDir)
	setVersion(t, "0.1.0")
	setUpgradeTarget(t, mustWriteFile(t, "baseloop", "old binary"))
	writeLockFile(t, stateDir, upgradeLock{PID: os.Getpid(), StartedAt: time.Now().UTC()})
	calls := stubTransport(t, func(url string) *http.Response {
		return httpBody(500, []byte("down"))
	})

	var out bytes.Buffer
	if code := Run([]string{"upgrade", "--background", "--json"}, &out, &out); code != 0 {
		t.Fatalf("expected quiet exit 0 on contention, got %d: %s", code, out.String())
	}
	if *calls != 0 {
		t.Fatalf("expected no download attempt while lock held, got %d calls", *calls)
	}
	if _, ok := readAutoUpdateFailure(); ok {
		t.Fatal("expected no failure record from a lock-contention exit")
	}
}

func TestManualUpgradeLockHeldFailsLoudAndKeepsRecord(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("BASELOOP_STATE", stateDir)
	setVersion(t, "0.1.0")
	setUpgradeTarget(t, mustWriteFile(t, "baseloop", "old binary"))
	recordAutoUpdateFailure("v0.2.0", "earlier failure", false)
	lockPath := writeLockFile(t, stateDir, upgradeLock{PID: os.Getpid(), StartedAt: time.Now().UTC()})

	// A leaked recursion marker must not flip a manual run into quiet child
	// mode: behavior is keyed to the --background flag alone.
	t.Setenv(upgradeChildEnvVar, "1")

	var out bytes.Buffer
	if code := Run([]string{"upgrade", "--json"}, &out, &out); code != 1 {
		t.Fatalf("expected loud exit 1 on contention, got %d: %s", code, out.String())
	}
	// Decode before comparing: JSON escapes Windows path backslashes.
	var envlp struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(out.Bytes(), &envlp); err != nil {
		t.Fatalf("invalid envelope %q: %v", out.String(), err)
	}
	if !strings.Contains(envlp.Error.Message, lockPath) {
		t.Fatalf("expected lock path in error, got %s", envlp.Error.Message)
	}
	if _, ok := readAutoUpdateFailure(); !ok {
		t.Fatal("expected failure record to survive a contention-failed manual run")
	}
}

func TestBackgroundUpgradeFailureWritesRecordManualDoesNot(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("BASELOOP_STATE", stateDir)
	t.Setenv("BASELOOP_RELEASES_API_URL", "https://rel.test/releases")
	setVersion(t, "0.1.0")
	setUpgradeTarget(t, mustWriteFile(t, "baseloop", "old binary"))
	writeVersionCheckCache(t, stateDir, "v0.2.0")
	stubTransport(t, func(url string) *http.Response {
		return httpBody(500, []byte("down"))
	})

	var out bytes.Buffer
	if code := Run([]string{"upgrade", "--background", "--json"}, &out, &out); code != 1 {
		t.Fatalf("expected exit 1, got %d: %s", code, out.String())
	}
	rec, ok := readAutoUpdateFailure()
	if !ok || rec.Target != "v0.2.0" || rec.Attempts != 1 || rec.Partial {
		t.Fatalf("expected full failure record for v0.2.0, got %+v ok=%v", rec, ok)
	}

	// The same failure on a manual run writes no record (the human saw it).
	clearAutoUpdateFailure()
	out.Reset()
	if code := Run([]string{"upgrade", "--json"}, &out, &out); code != 1 {
		t.Fatalf("expected exit 1, got %d: %s", code, out.String())
	}
	if _, ok := readAutoUpdateFailure(); ok {
		t.Fatal("expected no record from a manual failure")
	}
}

func TestBackgroundUpgradeTargetMissingAbortsSilently(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("BASELOOP_STATE", stateDir)
	setVersion(t, "0.1.0")
	setUpgradeTarget(t, filepath.Join(t.TempDir(), "gone-binary"))
	calls := stubTransport(t, func(url string) *http.Response {
		return httpBody(500, []byte("down"))
	})

	var out bytes.Buffer
	if code := Run([]string{"upgrade", "--background", "--json"}, &out, &out); code != 0 {
		t.Fatalf("expected exit 0, got %d: %s", code, out.String())
	}
	if *calls != 0 {
		t.Fatalf("expected no network activity, got %d calls", *calls)
	}
	for _, name := range []string{upgradeLockFile, autoUpdateFailureFile, autoUpdateLogFile} {
		if _, err := os.Stat(filepath.Join(stateDir, name)); !os.IsNotExist(err) {
			t.Fatalf("expected no %s after uninstall-race abort, got %v", name, err)
		}
	}
}

func TestUpgradeRejectsUnknownArgs(t *testing.T) {
	setVersion(t, "0.1.0")
	for _, args := range [][]string{
		{"upgrade", "--bogus"},
		{"upgrade", "--background", "extra"},
		{"upgrade", "extra"},
	} {
		var out bytes.Buffer
		if code := Run(append(args, "--json"), &out, &out); code != 2 {
			t.Fatalf("%v: expected exit 2, got %d: %s", args, code, out.String())
		}
	}
}

func TestBackgroundUpgradeDuplicateChildSkipsSwap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test fixture builds a unix archive")
	}
	stateDir := t.TempDir()
	t.Setenv("BASELOOP_STATE", stateDir)
	t.Setenv("BASELOOP_RELEASES_API_URL", "https://rel.test/releases")
	t.Setenv("BASELOOP_SKIP_SETUP", "1")
	setVersion(t, "0.1.0") // old code: its own version still looks outdated

	// The on-disk target already holds the new binary: a winner swapped it.
	target := mustWriteFile(t, "baseloop", "new binary 0.2.0")
	setUpgradeTarget(t, target)
	recordAutoUpdateFailure("v0.2.0", "earlier failure", false)

	archive := writeTarGzBytes(t, map[string]string{"baseloop": "new binary 0.2.0"})
	stubUpgradeRelease(t, "v0.2.0", archive, archive)

	var out bytes.Buffer
	if code := Run([]string{"upgrade", "--background", "--json"}, &out, &out); code != 0 {
		t.Fatalf("expected exit 0, got %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "Already up to date.") {
		t.Fatalf("expected duplicate child to report up to date, got %s", out.String())
	}
	if _, ok := readAutoUpdateFailure(); ok {
		t.Fatal("expected record cleared by the duplicate child")
	}
}

func TestManualUpgradeStartClearsRecordEvenWhenCurrent(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("BASELOOP_STATE", stateDir)
	t.Setenv("BASELOOP_RELEASES_API_URL", "https://rel.test/releases")
	setVersion(t, "0.2.0")
	setUpgradeTarget(t, mustWriteFile(t, "baseloop", "current binary"))
	recordAutoUpdateFailure("v0.2.0", "stale failure", false)
	stubTransport(t, func(url string) *http.Response {
		return httpBody(200, cliReleaseJSON(t, "v0.2.0", true))
	})

	var out bytes.Buffer
	if code := Run([]string{"upgrade", "--json"}, &out, &out); code != 0 {
		t.Fatalf("expected exit 0, got %d: %s", code, out.String())
	}
	if _, ok := readAutoUpdateFailure(); ok {
		t.Fatal("expected manual upgrade to clear the failure record")
	}
}

func mustWriteFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// autoUpdateTestEnv arranges the standard preconditions for spawn-hook tests:
// isolated config and state, an outdated binary, a fresh cache naming a newer
// release, a writable spawn target, an offline transport, and the spawn seam.
func autoUpdateTestEnv(t *testing.T) (stateDir string, spawns *int) {
	t.Helper()
	stateDir = t.TempDir()
	t.Setenv("BASELOOP_STATE", stateDir)
	t.Setenv("BASELOOP_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	// These tests assert real spawns; the suite itself runs under CI, where
	// the spawn guard would (correctly) block them.
	t.Setenv("CI", "")
	t.Setenv("BUILD_NUMBER", "")
	t.Setenv("RUN_ID", "")
	setVersion(t, "0.1.0")
	writeVersionCheckCache(t, stateDir, "v0.2.0")
	setUpgradeTarget(t, mustWriteFile(t, "baseloop", "old binary"))
	stubTransport(t, func(url string) *http.Response {
		return httpBody(500, []byte("down"))
	})
	return stateDir, setSpawnSeam(t)
}

func writeFailureRecord(t *testing.T, stateDir string, rec autoUpdateFailure) {
	t.Helper()
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, autoUpdateFailureFile), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func runOrdinary(t *testing.T) (stdout, stderr string) {
	t.Helper()
	var outBuf, errBuf bytes.Buffer
	Run([]string{"auth", "status", "--json"}, &outBuf, &errBuf)
	return outBuf.String(), errBuf.String()
}

func TestAutoUpdateSpawnsWhenClear(t *testing.T) {
	_, spawns := autoUpdateTestEnv(t)
	t.Setenv("BASELOOP_AUTO_UPDATE", "1")

	stdout, stderr := runOrdinary(t)
	if *spawns != 1 {
		t.Fatalf("expected one spawn, got %d (stderr %q)", *spawns, stderr)
	}
	if !strings.Contains(stderr, "Updating baseloop to v0.2.0 in the background") || !strings.Contains(stderr, autoUpdateLogFile) {
		t.Fatalf("expected spawn announcement with log path, got %q", stderr)
	}
	if strings.Contains(stderr, "Run: baseloop upgrade") {
		t.Fatalf("expected announcement to replace the nag, got %q", stderr)
	}
	if strings.Contains(stdout, "Updating baseloop") {
		t.Fatalf("expected stdout untouched, got %q", stdout)
	}
}

func TestAutoUpdateDisabledKeepsNag(t *testing.T) {
	_, spawns := autoUpdateTestEnv(t)

	_, stderr := runOrdinary(t)
	if *spawns != 0 {
		t.Fatalf("expected no spawn while disabled, got %d", *spawns)
	}
	if !strings.Contains(stderr, "Run: baseloop upgrade") {
		t.Fatalf("expected plain nag, got %q", stderr)
	}
}

func TestAutoUpdateBlockedGuardsFallBackToNag(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T)
	}{
		{"CI", func(t *testing.T) { t.Setenv("CI", "true") }},
		{"BUILD_NUMBER", func(t *testing.T) { t.Setenv("BUILD_NUMBER", "7") }},
		{"RUN_ID", func(t *testing.T) { t.Setenv("RUN_ID", "abc") }},
		{"releases URL override", func(t *testing.T) { t.Setenv("BASELOOP_RELEASES_API_URL", "https://rel.test/releases") }},
		{"repo override", func(t *testing.T) { t.Setenv("BASELOOP_REPO", "evil/mirror") }},
		{"package-manager path", func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "Cellar", "baseloop", "bin")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "baseloop")
			if err := os.WriteFile(path, []byte("brew binary"), 0o755); err != nil {
				t.Fatal(err)
			}
			setUpgradeTarget(t, path)
		}},
		{"target missing", func(t *testing.T) { setUpgradeTarget(t, filepath.Join(t.TempDir(), "gone")) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, spawns := autoUpdateTestEnv(t)
			t.Setenv("BASELOOP_AUTO_UPDATE", "1")
			c.setup(t)

			_, stderr := runOrdinary(t)
			if *spawns != 0 {
				t.Fatalf("expected no spawn, got %d", *spawns)
			}
			if !strings.Contains(stderr, "Run: baseloop upgrade") {
				t.Fatalf("expected fallback nag, got %q", stderr)
			}
		})
	}
}

func TestAutoUpdateUnwritableDirFallsBackToNag(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores permission bits")
	}
	_, spawns := autoUpdateTestEnv(t)
	t.Setenv("BASELOOP_AUTO_UPDATE", "1")
	dir := filepath.Join(t.TempDir(), "rootbin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "baseloop")
	if err := os.WriteFile(path, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	setUpgradeTarget(t, path)

	_, stderr := runOrdinary(t)
	if *spawns != 0 {
		t.Fatalf("expected no spawn into unwritable dir, got %d", *spawns)
	}
	if !strings.Contains(stderr, "Run: baseloop upgrade") {
		t.Fatalf("expected fallback nag, got %q", stderr)
	}
}

func TestAutoUpdateRecursionMarkerSilencesEverything(t *testing.T) {
	stateDir, spawns := autoUpdateTestEnv(t)
	t.Setenv("BASELOOP_AUTO_UPDATE", "1")
	t.Setenv(upgradeChildEnvVar, "1")
	writeFailureRecord(t, stateDir, autoUpdateFailure{Target: "v0.2.0", Attempts: 1, LastAttemptAt: time.Now().UTC(), Error: "boom"})

	_, stderr := runOrdinary(t)
	if stderr != "" {
		t.Fatalf("expected zero stderr under the recursion marker, got %q", stderr)
	}
	if *spawns != 0 {
		t.Fatalf("expected no spawn, got %d", *spawns)
	}
}

func TestAutoUpdateFailureNoticePrecedence(t *testing.T) {
	stateDir, spawns := autoUpdateTestEnv(t)
	t.Setenv("BASELOOP_AUTO_UPDATE", "1")
	writeFailureRecord(t, stateDir, autoUpdateFailure{Target: "v0.2.0", Attempts: 1, LastAttemptAt: time.Now().UTC(), Error: "download \x1b[31mboom\x1b[0m"})

	_, stderr := runOrdinary(t)
	if *spawns != 0 {
		t.Fatalf("expected blocked spawn, got %d", *spawns)
	}
	if !strings.Contains(stderr, "Auto-update to v0.2.0 failed, attempt 1 of 3") {
		t.Fatalf("expected failure notice, got %q", stderr)
	}
	if strings.Contains(stderr, "\x1b") {
		t.Fatalf("expected sanitized error text, got %q", stderr)
	}
	if strings.Contains(stderr, "A new baseloop release is available") {
		t.Fatalf("expected failure notice to replace the nag, got %q", stderr)
	}
}

func TestAutoUpdateFailureNoticeSuppressedWhenDisabled(t *testing.T) {
	stateDir, spawns := autoUpdateTestEnv(t)
	t.Setenv("BASELOOP_AUTO_UPDATE", "0")
	writeFailureRecord(t, stateDir, autoUpdateFailure{Target: "v0.2.0", Attempts: 1, LastAttemptAt: time.Now().UTC(), Error: "boom"})

	_, stderr := runOrdinary(t)
	if *spawns != 0 {
		t.Fatalf("expected no spawn, got %d", *spawns)
	}
	if strings.Contains(stderr, "Auto-update") {
		t.Fatalf("expected no failure notice while disabled, got %q", stderr)
	}
	if !strings.Contains(stderr, "Run: baseloop upgrade") {
		t.Fatalf("expected plain nag while disabled, got %q", stderr)
	}
}

func TestAutoUpdatePartialNoticePrintsEvenWhenCurrent(t *testing.T) {
	stateDir, spawns := autoUpdateTestEnv(t)
	setVersion(t, "0.2.0") // the swap landed: the running binary IS current
	writeFailureRecord(t, stateDir, autoUpdateFailure{Target: "v0.2.0", Attempts: 1, LastAttemptAt: time.Now().UTC(), Error: "claude missing", Partial: true})

	_, stderr := runOrdinary(t)
	if *spawns != 0 {
		t.Fatalf("expected no spawn, got %d", *spawns)
	}
	if !strings.Contains(stderr, "Run: baseloop setup skills") {
		t.Fatalf("expected partial-failure notice, got %q", stderr)
	}
}

func TestAutoUpdateOutOfBandRecoveryClearsRecord(t *testing.T) {
	stateDir, spawns := autoUpdateTestEnv(t)
	setVersion(t, "0.2.0") // manual reinstall already reached the target
	writeFailureRecord(t, stateDir, autoUpdateFailure{Target: "v0.2.0", Attempts: 2, LastAttemptAt: time.Now().UTC(), Error: "boom"})

	_, stderr := runOrdinary(t)
	if *spawns != 0 {
		t.Fatalf("expected no spawn, got %d", *spawns)
	}
	if stderr != "" {
		t.Fatalf("expected silence after out-of-band recovery, got %q", stderr)
	}
	if _, ok := readAutoUpdateFailure(); ok {
		t.Fatal("expected record cleared by out-of-band recovery")
	}
}

func TestAutoUpdateRetryWindowReArms(t *testing.T) {
	stateDir, spawns := autoUpdateTestEnv(t)
	t.Setenv("BASELOOP_AUTO_UPDATE", "1")
	writeFailureRecord(t, stateDir, autoUpdateFailure{Target: "v0.2.0", Attempts: 1, LastAttemptAt: time.Now().UTC().Add(-autoUpdateRetryAfter - time.Minute), Error: "boom"})

	_, stderr := runOrdinary(t)
	if *spawns != 1 {
		t.Fatalf("expected retry spawn after the window, got %d (stderr %q)", *spawns, stderr)
	}

	// Attempts exhausted: dormant regardless of age.
	writeFailureRecord(t, stateDir, autoUpdateFailure{Target: "v0.2.0", Attempts: autoUpdateMaxAttempts, LastAttemptAt: time.Now().UTC().Add(-72 * time.Hour), Error: "boom"})
	*spawns = 0
	_, stderr = runOrdinary(t)
	if *spawns != 0 {
		t.Fatalf("expected dormant record to block, got %d", *spawns)
	}
	if !strings.Contains(stderr, "attempt 3 of 3") {
		t.Fatalf("expected exhausted-attempts notice, got %q", stderr)
	}
}

func TestAutoUpdateNewerReleaseReArms(t *testing.T) {
	stateDir, spawns := autoUpdateTestEnv(t)
	t.Setenv("BASELOOP_AUTO_UPDATE", "1")
	// Record for an OLDER target than the cache: superseded, re-armed.
	writeFailureRecord(t, stateDir, autoUpdateFailure{Target: "v0.1.5", Attempts: 3, LastAttemptAt: time.Now().UTC(), Error: "boom"})

	_, stderr := runOrdinary(t)
	if *spawns != 1 {
		t.Fatalf("expected newer release to re-arm the spawn, got %d (stderr %q)", *spawns, stderr)
	}
}

func TestAutoUpdateInProgressNotice(t *testing.T) {
	stateDir, spawns := autoUpdateTestEnv(t)
	t.Setenv("BASELOOP_AUTO_UPDATE", "1")
	writeLockFile(t, stateDir, upgradeLock{PID: os.Getpid(), StartedAt: time.Now().UTC()})

	_, stderr := runOrdinary(t)
	if *spawns != 0 {
		t.Fatalf("expected no spawn while in flight, got %d", *spawns)
	}
	if !strings.Contains(stderr, "already in progress") {
		t.Fatalf("expected in-progress notice, got %q", stderr)
	}
	if strings.Contains(stderr, "Run: baseloop upgrade") {
		t.Fatalf("expected no nag while in flight (it would fail on the lock), got %q", stderr)
	}
}

// doctorAdvisoryEnv isolates everything the auto_update advisory reads so a
// developer machine's real config or CI's env vars cannot leak in.
func doctorAdvisoryEnv(t *testing.T) string {
	t.Helper()
	stateDir := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("HERMES_HOME", "")
	t.Setenv("BASELOOP_TOKEN", "")
	t.Setenv("BASELOOP_STATE", stateDir)
	t.Setenv("BASELOOP_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("CI", "")
	t.Setenv("BUILD_NUMBER", "")
	t.Setenv("RUN_ID", "")
	setVersion(t, "0.1.0")
	setUpgradeTarget(t, mustWriteFile(t, "baseloop", "binary"))
	stubTransport(t, func(url string) *http.Response {
		return httpBody(500, []byte("down"))
	})
	return stateDir
}

func runDoctor(t *testing.T) string {
	t.Helper()
	var out bytes.Buffer
	Run([]string{"doctor", "--json"}, &out, &out)
	return out.String()
}

func TestDoctorAutoUpdateAdvisoryStates(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		doctorAdvisoryEnv(t)
		out := runDoctor(t)
		if !strings.Contains(out, "auto_update") || !strings.Contains(out, "baseloop setup auto-update on") {
			t.Fatalf("expected disabled advisory with enable hint, got %s", out)
		}
	})

	t.Run("enabled and clear", func(t *testing.T) {
		doctorAdvisoryEnv(t)
		t.Setenv("BASELOOP_AUTO_UPDATE", "1")
		out := runDoctor(t)
		if !strings.Contains(out, "Auto-update is enabled.") {
			t.Fatalf("expected clean enabled advisory, got %s", out)
		}
	})

	t.Run("enabled but endpoint override blocks", func(t *testing.T) {
		doctorAdvisoryEnv(t)
		t.Setenv("BASELOOP_AUTO_UPDATE", "1")
		t.Setenv("BASELOOP_REPO", "evil/mirror")
		out := runDoctor(t)
		if !strings.Contains(out, "cannot run here") || !strings.Contains(out, "BASELOOP_REPO") {
			t.Fatalf("expected named persistent blocker, got %s", out)
		}
	})

	t.Run("failure record shown even when disabled", func(t *testing.T) {
		stateDir := doctorAdvisoryEnv(t)
		writeFailureRecord(t, stateDir, autoUpdateFailure{Target: "v0.2.0", Attempts: 2, LastAttemptAt: time.Now().UTC(), Error: "checksum \x1b[31mmismatch\x1b[0m"})
		out := runDoctor(t)
		if !strings.Contains(out, "attempt 2 of 3") || !strings.Contains(out, "Run baseloop upgrade") {
			t.Fatalf("expected failure advisory with attempts, got %s", out)
		}
		if strings.Contains(out, "\\u001b") || strings.Contains(out, "\x1b") {
			t.Fatalf("expected sanitized error in advisory, got %s", out)
		}
	})

	t.Run("partial record points at setup skills", func(t *testing.T) {
		stateDir := doctorAdvisoryEnv(t)
		writeFailureRecord(t, stateDir, autoUpdateFailure{Target: "v0.2.0", Attempts: 1, LastAttemptAt: time.Now().UTC(), Error: "claude missing", Partial: true})
		out := runDoctor(t)
		if !strings.Contains(out, "Run baseloop setup skills") {
			t.Fatalf("expected partial advisory, got %s", out)
		}
	})

	t.Run("fresh lock reports in progress", func(t *testing.T) {
		stateDir := doctorAdvisoryEnv(t)
		writeLockFile(t, stateDir, upgradeLock{PID: os.Getpid(), StartedAt: time.Now().UTC()})
		out := runDoctor(t)
		if !strings.Contains(out, "upgrade is in progress") {
			t.Fatalf("expected in-progress advisory, got %s", out)
		}
	})

	t.Run("stale lock mentions takeover", func(t *testing.T) {
		stateDir := doctorAdvisoryEnv(t)
		writeLockFile(t, stateDir, upgradeLock{PID: 1 << 30, StartedAt: time.Now().UTC().Add(-upgradeLockStaleAfter - time.Minute)})
		out := runDoctor(t)
		if !strings.Contains(out, "stale upgrade lock") {
			t.Fatalf("expected stale-lock advisory, got %s", out)
		}
	})

	t.Run("advisory never flips doctor exit", func(t *testing.T) {
		stateDir := doctorAdvisoryEnv(t)
		t.Setenv("BASELOOP_TOKEN", "tok")
		// Satisfy the non-advisory checks so the only not-ok entry is the
		// auto_update advisory itself.
		if _, err := installBaseloopClaudeSkill(); err != nil {
			t.Fatal(err)
		}
		pluginsDir := filepath.Join(homeDir(), ".claude", "plugins")
		if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(pluginsDir, "installed_plugins.json"), []byte(`{"plugins":["baseloop-gtm@1"]}`), 0o644); err != nil {
			t.Fatal(err)
		}
		writeFailureRecord(t, stateDir, autoUpdateFailure{Target: "v0.2.0", Attempts: 3, LastAttemptAt: time.Now().UTC(), Error: "down"})
		stubTransport(t, func(url string) *http.Response {
			return httpBody(200, []byte(`{"ok":true,"data":{}}`))
		})
		var out bytes.Buffer
		if code := Run([]string{"doctor", "--json"}, &out, &out); code != 0 {
			t.Fatalf("expected advisory-only issues to keep doctor exit 0, got %d: %s", code, out.String())
		}
		if !strings.Contains(out.String(), "attempt 3 of 3") {
			t.Fatalf("expected the failure advisory present, got %s", out.String())
		}
	})
}

func TestBackgroundUpgradeAbortsWhenLockUsurpedPreSwap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test fixture builds a unix archive")
	}
	stateDir := t.TempDir()
	t.Setenv("BASELOOP_STATE", stateDir)
	t.Setenv("BASELOOP_RELEASES_API_URL", "https://rel.test/releases")
	t.Setenv("BASELOOP_SKIP_SETUP", "1")
	setVersion(t, "0.1.0")
	target := mustWriteFile(t, "baseloop", "old binary")
	setUpgradeTarget(t, target)

	archive := writeTarGzBytes(t, map[string]string{"baseloop": "new binary 0.2.0"})
	sum := sha256.Sum256(archive)
	checksums := []byte(hex.EncodeToString(sum[:]) + "  " + platformAssetName("v0.2.0") + "\n")
	releases := cliReleaseJSON(t, "v0.2.0", true)
	stubTransport(t, func(url string) *http.Response {
		switch {
		case strings.Contains(url, "/releases"):
			return httpBody(200, releases)
		case strings.Contains(url, "checksums.txt"):
			return httpBody(200, checksums)
		case strings.Contains(url, platformAssetName("v0.2.0")):
			// Mid-download takeover: by the time this child reaches the
			// pre-swap ownership re-check, the lock belongs to someone else.
			writeLockFile(t, stateDir, upgradeLock{PID: os.Getpid() + 1, StartedAt: time.Now().UTC()})
			return httpBody(200, archive)
		default:
			return httpBody(500, []byte("unexpected: "+url))
		}
	})

	var out bytes.Buffer
	if code := Run([]string{"upgrade", "--background", "--json"}, &out, &out); code != 0 {
		t.Fatalf("expected quiet exit 0, got %d: %s", code, out.String())
	}
	kept, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(kept) != "old binary" {
		t.Fatalf("expected usurped child to leave the binary alone, got %q", kept)
	}
	if _, ok := readAutoUpdateFailure(); ok {
		t.Fatal("expected no failure record from a usurped exit")
	}
	if got, ok := readUpgradeLock(filepath.Join(stateDir, upgradeLockFile)); !ok || got.PID != os.Getpid()+1 {
		t.Fatalf("expected the usurper's lock to survive, got %+v ok=%v", got, ok)
	}
}

func TestBackgroundUpgradeSetupFailureWritesPartialRecord(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test fixture builds a unix archive")
	}
	stateDir := t.TempDir()
	t.Setenv("BASELOOP_STATE", stateDir)
	t.Setenv("BASELOOP_RELEASES_API_URL", "https://rel.test/releases")
	t.Setenv("BASELOOP_SKIP_SETUP", "") // the real failure path, not the skip
	setVersion(t, "0.1.0")
	target := mustWriteFile(t, "baseloop", "old binary")
	setUpgradeTarget(t, target)

	// The swapped-in "binary" is a script whose setup skills always fails.
	archive := writeTarGzBytes(t, map[string]string{"baseloop": "#!/bin/sh\nexit 1\n"})
	stubUpgradeRelease(t, "v0.2.0", archive, archive)

	var out bytes.Buffer
	if code := Run([]string{"upgrade", "--background", "--json"}, &out, &out); code != 0 {
		t.Fatalf("expected exit 0 (swap landed), got %d: %s", code, out.String())
	}
	rec, ok := readAutoUpdateFailure()
	if !ok || !rec.Partial || rec.Target != "v0.2.0" {
		t.Fatalf("expected partial record for v0.2.0, got %+v ok=%v", rec, ok)
	}
}

func TestAutoUpdatePartialRecordSupersededByNewerRelease(t *testing.T) {
	stateDir, spawns := autoUpdateTestEnv(t)
	// The partial swap to v0.2.0 landed (we run it), and v0.3.0 has since
	// shipped: the newer release supersedes the partial record.
	setVersion(t, "0.2.0")
	writeVersionCheckCache(t, stateDir, "v0.3.0")
	t.Setenv("BASELOOP_AUTO_UPDATE", "1")
	writeFailureRecord(t, stateDir, autoUpdateFailure{Target: "v0.2.0", Attempts: 1, LastAttemptAt: time.Now().UTC(), Error: "claude missing", Partial: true})

	_, stderr := runOrdinary(t)
	if *spawns != 1 {
		t.Fatalf("expected newer release to supersede the partial record and spawn, got %d (stderr %q)", *spawns, stderr)
	}
	if strings.Contains(stderr, "setup skills") {
		t.Fatalf("expected no partial notice when superseded, got %q", stderr)
	}
}

func TestManualUpgradeCreatesMissingStateDir(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "state")
	t.Setenv("BASELOOP_STATE", missing)
	t.Setenv("BASELOOP_RELEASES_API_URL", "https://rel.test/releases")
	setVersion(t, "0.2.0")
	setUpgradeTarget(t, mustWriteFile(t, "baseloop", "current binary"))
	stubTransport(t, func(url string) *http.Response {
		return httpBody(200, cliReleaseJSON(t, "v0.2.0", true))
	})

	var out bytes.Buffer
	if code := Run([]string{"upgrade", "--json"}, &out, &out); code != 0 {
		t.Fatalf("expected manual upgrade to succeed on a fresh install, got %d: %s", code, out.String())
	}
	if _, err := os.Stat(missing); err != nil {
		t.Fatalf("expected manual upgrade to create the state dir, got %v", err)
	}
}

func TestAutoUpdateUnwritableStateDirFallsBackToNag(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores permission bits")
	}
	stateDir, spawns := autoUpdateTestEnv(t)
	t.Setenv("BASELOOP_AUTO_UPDATE", "1")
	if err := os.Chmod(stateDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(stateDir, 0o755) })

	_, stderr := runOrdinary(t)
	if *spawns != 0 {
		t.Fatalf("expected no spawn with an unwritable state dir (dormancy would be defeated), got %d", *spawns)
	}
	if !strings.Contains(stderr, "Run: baseloop upgrade") {
		t.Fatalf("expected fallback nag, got %q", stderr)
	}
}

func TestBackgroundUpgradeDuplicateChildPreservesPartialRecord(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test fixture builds a unix archive")
	}
	stateDir := t.TempDir()
	t.Setenv("BASELOOP_STATE", stateDir)
	t.Setenv("BASELOOP_RELEASES_API_URL", "https://rel.test/releases")
	t.Setenv("BASELOOP_SKIP_SETUP", "1")
	setVersion(t, "0.1.0") // delayed duplicate runs the OLD binary's code

	// The winner already swapped the binary but its plugin refresh failed.
	target := mustWriteFile(t, "baseloop", "new binary 0.2.0")
	setUpgradeTarget(t, target)
	writeFailureRecord(t, stateDir, autoUpdateFailure{Target: "v0.2.0", Attempts: 1, LastAttemptAt: time.Now().UTC(), Error: "claude missing", Partial: true})

	archive := writeTarGzBytes(t, map[string]string{"baseloop": "new binary 0.2.0"})
	stubUpgradeRelease(t, "v0.2.0", archive, archive)

	var out bytes.Buffer
	if code := Run([]string{"upgrade", "--background", "--json"}, &out, &out); code != 0 {
		t.Fatalf("expected exit 0, got %d: %s", code, out.String())
	}
	rec, ok := readAutoUpdateFailure()
	if !ok || !rec.Partial {
		t.Fatalf("expected partial record to survive a duplicate child (it skips setup), got %+v ok=%v", rec, ok)
	}
}
