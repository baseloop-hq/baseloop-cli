// Self-update: the `baseloop upgrade` command and the throttled version check
// doctor consumes.
//
// Distribution context: installs are one-shot (curl | bash), so without this
// file an installed CLI — and the Claude setup behind it — would never
// refresh. Doctor surfaces a `cli_version` advisory (Claude runs doctor before
// multi-step work, so the advisory is the fleet's update signal), and
// `baseloop upgrade` performs the swap: resolve latest release, verify the
// archive against the release's checksums.txt (mandatory here, unlike the
// plugin marketplace refresh, because this replaces the binary itself),
// stage next to the current binary, rename into place, then re-run plugin
// setup via the NEW binary.
package cli

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
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/baseloop-hq/baseloop-cli/internal/config"
	"github.com/baseloop-hq/baseloop-cli/internal/output"
	"github.com/baseloop-hq/baseloop-cli/internal/state"
	"github.com/baseloop-hq/baseloop-cli/internal/version"
)

const (
	defaultCLIRepo = "baseloop-hq/baseloop-cli"
	// versionCheckTTL throttles the doctor-side release lookup so doctor stays
	// fast and offline-safe between refreshes.
	versionCheckTTL = 24 * time.Hour
	// versionCheckFile caches the last release lookup in the state dir.
	versionCheckFile = "version-check.json"
	// postUpgradeSetupTimeout bounds the post-upgrade agent setup subprocess.
	// The upgrade itself is already complete by the time this runs. Worst
	// case inside `setup skills`: two agent legs (Claude, Codex) of up to
	// three plugin commands each, every command holding its own 2-minute
	// budget (agentPluginTimeout), so 2 x 3 x 2min = 12 minutes plus
	// overhead. 15 minutes is a ceiling, not an expected duration.
	postUpgradeSetupTimeout = 15 * time.Minute
)

// upgradeTargetPath resolves the on-disk binary the upgrade replaces. A
// package var so tests can point the swap at a scratch file instead of the
// test binary that os.Executable reports under `go test`.
var upgradeTargetPath = func() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		return resolved, nil
	}
	return exe, nil
}

// cliReleasesAPIURL returns the GitHub releases endpoint for the CLI binary.
// BASELOOP_RELEASES_API_URL overrides the whole endpoint (tests, mirrors);
// BASELOOP_REPO swaps the repo like the install scripts do.
func cliReleasesAPIURL() string {
	if u := strings.TrimSpace(os.Getenv("BASELOOP_RELEASES_API_URL")); u != "" {
		return u
	}
	repo := strings.TrimSpace(os.Getenv("BASELOOP_REPO"))
	if repo == "" {
		repo = defaultCLIRepo
	}
	return "https://api.github.com/repos/" + repo + "/releases"
}

// resolveLatestCLIRelease returns the newest non-prerelease that ships a
// binary archive for this platform: its tag, the archive URL, and the
// checksums.txt URL ("" when the release publishes none).
func resolveLatestCLIRelease(ctx context.Context) (tag, assetURL, checksumsURL string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cliReleasesAPIURL(), nil)
	if err != nil {
		return "", "", "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "baseloop-cli")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", "", err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", "", "", fmt.Errorf("GitHub releases lookup returned HTTP %d", res.StatusCode)
	}
	var releases []githubRelease
	if err := json.NewDecoder(res.Body).Decode(&releases); err != nil {
		return "", "", "", err
	}
	tag, assetURL, checksumsURL = selectCLIAsset(releases, runtime.GOOS, runtime.GOARCH)
	if assetURL == "" {
		return "", "", "", fmt.Errorf("no release asset found for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	return tag, assetURL, checksumsURL, nil
}

// selectCLIAsset picks the newest non-prerelease carrying a
// baseloop_<version>_<goos>_<goarch> archive, mirroring the release artifact
// naming in DISTRIBUTION.md.
func selectCLIAsset(releases []githubRelease, goos, goarch string) (tag, assetURL, checksumsURL string) {
	platform := "_" + goos + "_" + goarch
	for _, release := range releases {
		if release.Prerelease {
			continue
		}
		var matched, checksums string
		for _, asset := range release.Assets {
			if asset.BrowserDownloadURL == "" {
				continue
			}
			name := strings.ToLower(asset.Name)
			if name == "checksums.txt" {
				checksums = asset.BrowserDownloadURL
				continue
			}
			if !strings.HasPrefix(name, "baseloop_") {
				continue
			}
			// Exact platform suffix: a bare Contains would let _linux_arm
			// match a _linux_arm64 asset.
			if strings.HasSuffix(name, platform+".tar.gz") || strings.HasSuffix(name, platform+".zip") {
				matched = asset.BrowserDownloadURL
			}
		}
		if matched != "" {
			return release.TagName, matched, checksums
		}
	}
	return "", "", ""
}

// versionOutdated reports whether latest is a strictly newer release than
// current. Versions compare as dot-separated numeric segments after an
// optional leading "v"; anything unparsable (including "dev") compares as not
// outdated, so odd tags can never nag or trigger a swap.
func versionOutdated(current, latest string) bool {
	cur, okCur := versionParts(current)
	lat, okLat := versionParts(latest)
	if !okCur || !okLat {
		return false
	}
	for i := 0; i < len(cur) || i < len(lat); i++ {
		var c, l int
		if i < len(cur) {
			c = cur[i]
		}
		if i < len(lat) {
			l = lat[i]
		}
		if l != c {
			return l > c
		}
	}
	return false
}

func versionParts(v string) ([]int, bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	// Drop pre-release/build suffixes so "0.2.0-rc1" still yields the core.
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	if v == "" {
		return nil, false
	}
	segments := strings.Split(v, ".")
	parts := make([]int, 0, len(segments))
	for _, s := range segments {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			return nil, false
		}
		parts = append(parts, n)
	}
	return parts, true
}

// versionCheck is the cached result of the last release lookup. Latest is ""
// when the lookup failed, which still counts as a check: offline machines
// wait out the TTL instead of paying a network timeout on every doctor run.
type versionCheck struct {
	CheckedAt time.Time `json:"checked_at"`
	Latest    string    `json:"latest"`
}

func versionCheckPath() (string, error) { return statePath(versionCheckFile) }

func readVersionCheck() (versionCheck, bool) {
	var check versionCheck
	path, err := versionCheckPath()
	if err != nil {
		return check, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return check, false
	}
	if err := json.Unmarshal(data, &check); err != nil {
		return check, false
	}
	return check, true
}

// writeVersionCheck is best-effort bookkeeping; a read-only state dir must
// never fail doctor or upgrade. Atomic (temp + rename) because the detached
// auto-update child and foreground commands can now write it concurrently,
// and a torn read would trigger a spurious refetch.
func writeVersionCheck(latest string) {
	path, err := versionCheckPath()
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	data, err := json.Marshal(versionCheck{CheckedAt: time.Now().UTC(), Latest: latest})
	if err != nil {
		return
	}
	_ = writeStateFileAtomic(path, data)
}

// latestReleaseCached returns the latest known release tag, refreshing the
// TTL'd cache with the given lookup timeout when stale. ok is false when the
// check does not apply (dev builds, opt-out) or no release is known. Lookup
// failures are cached like results, so an offline machine pays at most one
// timeout per TTL window.
func latestReleaseCached(timeout time.Duration) (string, bool) {
	if updateChecksDisabled() {
		return "", false
	}
	check, found := readVersionCheck()
	if !found || time.Since(check.CheckedAt) > versionCheckTTL {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		latest, _, _, err := resolveLatestCLIRelease(ctx)
		if err != nil {
			latest = ""
		}
		writeVersionCheck(latest)
		check.Latest = latest
	}
	if check.Latest == "" {
		return "", false
	}
	return check.Latest, true
}

// cliVersionAdvisory returns the doctor `cli_version` advisory: ok is true
// when the CLI is current, hint says how to upgrade, and show is false when
// the check does not apply (dev builds, opt-out, or no release known yet).
func cliVersionAdvisory() (ok bool, hint string, show bool) {
	latest, known := latestReleaseCached(5 * time.Second)
	if !known {
		return true, "", false
	}
	if versionOutdated(version.Version, latest) {
		return false, fmt.Sprintf("Run baseloop upgrade to get %s (current %s).", latest, version.Version), true
	}
	return true, "Run baseloop upgrade when a new release is available.", true
}

// updateChecksDisabled reports the two global update-pipeline kill switches:
// dev builds never check or nag, and BASELOOP_NO_UPDATE_CHECK turns off every
// update surface (notice, auto-update, doctor advisories).
func updateChecksDisabled() bool {
	return version.Version == "dev" || os.Getenv("BASELOOP_NO_UPDATE_CHECK") != ""
}

// setupAutoUpdate flips or reports the auto_update config key. Note that
// `setup` is deliberately not in the update-notice exclusion list, so enabling
// can trigger a background upgrade as soon as this very command exits:
// "enable" means "start updating", not "start updating eventually".
func setupAutoUpdate(args []string, g globals, stdout io.Writer) int {
	if len(args) == 0 {
		payload := autoUpdateStatePayload()
		summary := "Auto-update is disabled. Enable with: baseloop setup auto-update on"
		if effective, _ := payload["effective"].(bool); effective {
			summary = "Auto-update is enabled."
		}
		return render(stdout, g, output.Success(payload, summary, nil), 0)
	}
	if len(args) > 1 {
		return render(stdout, g, output.Failure("USAGE", "setup auto-update takes at most one argument", "Use baseloop setup auto-update [on|off].", nil), 2)
	}
	var value bool
	switch args[0] {
	case "on":
		value = true
	case "off":
		value = false
	default:
		return render(stdout, g, output.Failure("USAGE", "unknown setup auto-update argument: "+args[0], "Use baseloop setup auto-update [on|off].", nil), 2)
	}
	cfg, err := config.Load()
	if err != nil {
		return render(stdout, g, output.Failure("CONFIG_ERROR", err.Error(), "Fix or remove "+mustConfigPath()+", then retry.", nil), 1)
	}
	cfg.AutoUpdate = value
	if err := config.Save(cfg); err != nil {
		return render(stdout, g, output.Failure("CONFIG_ERROR", err.Error(), "", nil), 1)
	}
	summary := "Auto-update enabled. New releases install in the background; the next command may already trigger one."
	if !value {
		summary = "Auto-update disabled. New releases surface as a stderr notice instead."
	}
	return render(stdout, g, output.Success(autoUpdateStatePayload(), summary, nil), 0)
}

// autoUpdateEnabled is the effective auto-update switch over a fresh config
// load. The precedence itself lives in effectiveAutoUpdate so the state
// payload below can apply identical rules to a config it already loaded.
func autoUpdateEnabled() bool {
	cfg, err := config.Load()
	return effectiveAutoUpdate(cfg, err)
}

// effectiveAutoUpdate is the single home of the precedence chain:
// BASELOOP_NO_UPDATE_CHECK kills the whole update pipeline, the
// BASELOOP_AUTO_UPDATE env var overrides config in both directions, and a
// corrupt config (loadErr non-nil) reads as off — a broken file must never
// opt a machine into executing downloaded binaries.
func effectiveAutoUpdate(cfg config.Config, loadErr error) bool {
	if os.Getenv("BASELOOP_NO_UPDATE_CHECK") != "" {
		return false
	}
	if v, ok := autoUpdateEnvOverride(); ok {
		return v
	}
	return loadErr == nil && cfg.AutoUpdate
}

// autoUpdateEnvOverride returns the parsed BASELOOP_AUTO_UPDATE override and
// whether one is in effect (unset or unparseable values fall through to
// config).
func autoUpdateEnvOverride() (value, ok bool) {
	raw := os.Getenv("BASELOOP_AUTO_UPDATE")
	if raw == "" {
		return false, false
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false, false
	}
	return v, true
}

// autoUpdateStatePayload reports each layer of the switch separately so a
// user can see WHY auto-update is or is not effective, not just the result.
func autoUpdateStatePayload() map[string]any {
	cfg, err := config.Load()
	payload := map[string]any{"effective": effectiveAutoUpdate(cfg, err)}
	if err != nil {
		payload["config"] = false
		payload["config_error"] = err.Error()
	} else {
		payload["config"] = cfg.AutoUpdate
	}
	if raw := os.Getenv("BASELOOP_AUTO_UPDATE"); raw != "" {
		payload["env_override"] = raw
	}
	if os.Getenv("BASELOOP_NO_UPDATE_CHECK") != "" {
		payload["update_check_disabled"] = true
	}
	return payload
}

// autoUpdateAdvisory is doctor's always-on surface for auto-update state.
// Unlike the post-command notice, it reports the failure record even when
// auto-update has since been disabled, and it names the specific persistent
// blocker when auto-update is enabled but can never spawn — otherwise a
// mirror-installed fleet with BASELOOP_REPO set looks "enabled" while
// silently drifting stale. Hidden entirely for dev builds and the global
// opt-out, like cliVersionAdvisory.
func autoUpdateAdvisory() (ok bool, hint string, show bool) {
	if updateChecksDisabled() {
		return true, "", false
	}
	if rec, hasRecord := readAutoUpdateFailure(); hasRecord {
		age := time.Since(rec.LastAttemptAt).Round(time.Minute)
		if rec.Partial {
			return false, fmt.Sprintf("Auto-update installed %s but the Claude plugin refresh failed %s ago: %s. Run baseloop setup skills.", sanitizeRecordText(rec.Target), age, sanitizeRecordText(rec.Error)), true
		}
		return false, fmt.Sprintf("Auto-update to %s failed (attempt %d of %d, %s ago): %s. Run baseloop upgrade.", sanitizeRecordText(rec.Target), rec.Attempts, autoUpdateMaxAttempts, age, sanitizeRecordText(rec.Error)), true
	}
	if lockPath, err := upgradeLockPath(); err == nil {
		if _, statErr := os.Stat(lockPath); statErr == nil {
			if upgradeLockIsStale(lockPath) {
				return true, "A stale upgrade lock is present (" + lockPath + "); the next upgrade takes it over automatically.", true
			}
			detail := ""
			if lk, lkOK := readUpgradeLock(lockPath); lkOK {
				detail = " (started " + time.Since(lk.StartedAt).Round(time.Second).String() + " ago)"
			}
			return true, "An upgrade is in progress" + detail + ".", true
		}
	}
	if !autoUpdateEnabled() {
		return true, "Auto-update is disabled. Enable background updates: baseloop setup auto-update on", true
	}
	if reason := spawnBlockReason(); reason != "" {
		return false, "Auto-update is enabled but cannot run here: " + reason + ".", true
	}
	return true, "Auto-update is enabled.", true
}

// maybeAutoUpdate is the post-dispatch update hook: at most one stderr line
// per run, and — when the operator opted in — the detached background upgrade
// spawn. Without opt-in it is exactly the old one-line reminder (the
// npm/Homebrew pattern: the signal reaches every machine that uses the CLI at
// all, not just ones where doctor runs). It rides the same TTL'd cache as the
// doctor advisory, with a shorter lookup timeout so the once-per-TTL refresh
// can never hang a working command for long.
//
// Evaluation order is load-bearing:
//  1. Silence guards — excluded commands, the upgrade pipeline's own
//     subprocesses (recursion marker), dev builds, global opt-out.
//  2. Failure-record handling, deliberately BEFORE the newer-release check:
//     after a swap-succeeded-setup-failed upgrade the running binary IS
//     current, so gating on "newer release known" would silence the partial
//     notice in exactly its target scenario and never clear recovered records.
//  3. The newer-release check, then enablement, the record's retry policy,
//     the in-flight lock, and the environment guards — every blocked guard
//     falls back to the plain nag except the lock, whose honest message is
//     "in progress", not "run upgrade" (which would fail on contention).
func maybeAutoUpdate(cmd string, w io.Writer) {
	// Update-lifecycle and introspection commands are excluded: they either
	// handle versions themselves or are commonly scripted bare.
	switch cmd {
	case "upgrade", "doctor", "uninstall", "version", "--version", "commands", "help", "--help", "-h":
		return
	}
	if os.Getenv(upgradeChildEnvVar) != "" {
		// A notice here would leak into runPostUpgradeSetup's captured output
		// and resurface as an upgrade note — and a spawn would recurse.
		return
	}
	if updateChecksDisabled() {
		return
	}

	record, hasRecord := readAutoUpdateFailure()
	if hasRecord && !record.Partial {
		if _, parses := versionParts(record.Target); parses && !versionOutdated(version.Version, record.Target) {
			// Out-of-band recovery: a reinstall or manual fix already
			// reached the recorded target, so the record is obsolete.
			clearAutoUpdateFailure()
			record, hasRecord = autoUpdateFailure{}, false
		}
	}
	latest, known := latestReleaseCached(2 * time.Second)
	if hasRecord && record.Partial {
		// A release strictly newer than the partially-updated one supersedes
		// the record: that upgrade re-runs setup skills and clears it on
		// success, so falling through keeps one bad plugin refresh from
		// freezing a fleet on its current version forever. Otherwise the
		// partial notice prints regardless of enablement — and even though
		// the running binary is current, which is exactly its target
		// scenario (the swap landed, the plugin did not).
		if !known || !versionOutdated(record.Target, latest) {
			fmt.Fprintf(w, "\nbaseloop updated to %s, but the Claude plugin refresh failed. Run: baseloop setup skills\n", sanitizeRecordText(record.Target))
			return
		}
	}
	if !known || !versionOutdated(version.Version, latest) {
		return
	}
	if !autoUpdateEnabled() {
		printUpdateNag(w, latest)
		return
	}
	if hasRecord && record.blocksTarget(latest) {
		detail := sanitizeRecordText(record.Error)
		if detail != "" {
			detail = " (" + detail + ")"
		}
		fmt.Fprintf(w, "\nAuto-update to %s failed, attempt %d of %d%s. Run: baseloop upgrade\n", sanitizeRecordText(latest), record.Attempts, autoUpdateMaxAttempts, detail)
		return
	}
	if freshUpgradeLockExists() {
		fmt.Fprintf(w, "\nA baseloop update to %s is already in progress.\n", sanitizeRecordText(latest))
		return
	}
	if reason := spawnBlockReason(); reason != "" {
		// Permanently blocked environments (CI, mirrors, package-manager
		// installs, unwritable dirs) keep today's behavior; doctor names the
		// specific blocker so "enabled but never updates" is diagnosable.
		printUpdateNag(w, latest)
		return
	}
	if err := spawnBackgroundUpgrade(); err != nil {
		printUpdateNag(w, latest)
		return
	}
	logPath, _ := statePath(autoUpdateLogFile)
	fmt.Fprintf(w, "\nUpdating baseloop to %s in the background (log: %s).\n", sanitizeRecordText(latest), logPath)
}

// printUpdateNag sanitizes the tag: it normally comes from the canonical
// endpoint, but the cache may have been written under a user-set endpoint
// override, and a tag is still network-derived text.
func printUpdateNag(w io.Writer, latest string) {
	fmt.Fprintf(w, "\nA new baseloop release is available (%s -> %s). Run: baseloop upgrade\n", version.Version, sanitizeRecordText(latest))
}

// spawnBlockReason reports why this environment must not spawn a background
// upgrade, or "" when clear. Doctor surfaces the same reason so an operator
// can see why an enabled fleet is not updating.
func spawnBlockReason() string {
	if os.Getenv("CI") != "" || os.Getenv("BUILD_NUMBER") != "" || os.Getenv("RUN_ID") != "" {
		return "running under CI"
	}
	// The endpoint pin: the automatic path resolves releases only from the
	// canonical repo. Two injected env vars must never be able to redirect an
	// unattended binary swap; manual `baseloop upgrade` still honors them.
	if os.Getenv("BASELOOP_RELEASES_API_URL") != "" {
		return "BASELOOP_RELEASES_API_URL is set; the automatic path only trusts the canonical endpoint"
	}
	if repo := strings.TrimSpace(os.Getenv("BASELOOP_REPO")); repo != "" && repo != defaultCLIRepo {
		return "BASELOOP_REPO overrides the release repo; the automatic path only trusts " + defaultCLIRepo
	}
	// A state dir that cannot accept the lock and failure record defeats
	// dormancy: the child would fail, record nothing, and every subsequent
	// command would spawn another doomed download behind a false
	// announcement. No state writes, no spawn.
	if dir, err := state.Dir(); err != nil || !dirWritable(dir) {
		return "state directory is not writable"
	}
	target, err := upgradeTargetPath()
	if err != nil {
		return "cannot locate the running binary"
	}
	// Pure string check before any further syscalls: on a managed install
	// this guard fires on every spawn-eligible command.
	if packageManagerManagedPath(target) {
		return "binary at " + target + " is managed by a package manager"
	}
	if _, err := os.Stat(target); err != nil {
		return "target binary not found at " + target
	}
	if !dirWritable(filepath.Dir(target)) {
		return "install directory " + filepath.Dir(target) + " is not writable"
	}
	return ""
}

// packageManagerManagedPath detects installs owned by a package manager.
// upgradeTargetPath resolves symlinks and manager dirs are often
// user-writable (Homebrew on macOS), so a writability probe alone would
// happily clobber a brew/asdf/nix-managed binary that the manager will
// fight over later. Conservative by design: a false positive only means the
// machine keeps the nag.
func packageManagerManagedPath(path string) bool {
	p := strings.ToLower(filepath.ToSlash(path))
	for _, marker := range []string{"/cellar/", "/opt/homebrew/", "/linuxbrew/", "/nix/store/", "/.asdf/", "/mise/installs/", "/scoop/"} {
		if strings.Contains(p, marker) {
			return true
		}
	}
	return false
}

// dirWritable probes by creating and removing a temp file. Mode bits would
// lie on Windows ACLs, and "root-owned /usr/local/bin" is exactly the case
// that must fall back to the nag instead of a daily doomed download.
func dirWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".baseloop-probe-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}

func upgrade(args []string, g globals, stdout io.Writer) int {
	// --background is the (hidden) child mode the auto-update spawner uses:
	// quiet on contention, failure-record bookkeeping, log-file etiquette.
	// Exactly that one flag is accepted; everything else keeps the strict
	// rejection so no permissive parser can silently swallow unknown input.
	// The flag — not the env marker — switches behavior, so a
	// BASELOOP_UPGRADE_CHILD=1 leaked into a shell cannot make a manual
	// upgrade exit silently.
	background := false
	switch {
	case len(args) == 0:
	case len(args) == 1 && args[0] == "--background":
		background = true
	default:
		return render(stdout, g, output.Failure("USAGE", "upgrade takes no arguments", "Run baseloop upgrade.", nil), 2)
	}
	if version.Version == "dev" {
		return render(stdout, g, output.Failure("UNSUPPORTED", "This is a development build; upgrade only manages release installs.", "Install a release: curl -fsSL https://app.baseloop.io/install-cli | bash", nil), 1)
	}
	target, err := upgradeTargetPath()
	if err != nil {
		return render(stdout, g, output.Failure("UPGRADE_FAILED", "Could not locate the running binary: "+err.Error(), "", nil), 1)
	}
	// Uninstall race: in background mode the target-exists check runs BEFORE
	// the lock or any state write. A child that lost the race to uninstall
	// must leave nothing behind — acquiring the lock or recording a failure
	// would resurrect the state dir uninstall just deleted.
	if background {
		if _, err := os.Stat(target); err != nil {
			return render(stdout, g, output.Success(map[string]any{"skipped": "target-missing"}, "Target binary is gone (uninstalled?); nothing to upgrade.", nil), 0)
		}
	}
	if !background {
		// The lock never creates the state dir — that rule protects the
		// uninstall race for detached children (R10) — but a human running
		// upgrade on a fresh or BASELOOP_NO_UPDATE_CHECK install may predate
		// every other state write, so create the dir here rather than failing
		// a healthy manual upgrade on ENOENT.
		if dir, dirErr := state.Dir(); dirErr == nil {
			_ = os.MkdirAll(dir, 0o700)
		}
	}
	lock, err := acquireUpgradeLock()
	if err != nil {
		if background {
			// Quiet exit 0: a concurrent winner is doing the work (or the
			// state dir is unusable, in which case there is nothing useful a
			// detached child can do or report).
			return render(stdout, g, output.Success(map[string]any{"skipped": "lock"}, "Skipping: "+err.Error(), nil), 0)
		}
		var held *lockHeldError
		if errors.As(err, &held) {
			return render(stdout, g, output.Failure("UPGRADE_IN_PROGRESS", err.Error(), "Retry shortly; if no upgrade is actually running, the stale lock is taken over automatically after "+upgradeLockStaleAfter.String()+".", nil), 1)
		}
		return render(stdout, g, output.Failure("UPGRADE_FAILED", "Could not coordinate the upgrade: "+err.Error(), "", nil), 1)
	}
	defer lock.release()
	if background {
		// The lock winner owns the log: truncate so it holds exactly this
		// attempt. The spawner opened our stdio O_APPEND, so writes continue
		// from the new end — and loser spawns appending later can never zero
		// what the winner writes.
		if logPath, pathErr := statePath(autoUpdateLogFile); pathErr == nil {
			_ = os.Truncate(logPath, 0)
		}
	} else {
		// A human running upgrade is the recovery path for a recorded
		// failure. Cleared only after lock acquisition: clearing before would
		// erase legitimate failure state on a run that then dies on contention.
		clearAutoUpdateFailure()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	tag, assetURL, checksumsURL, err := resolveLatestCLIRelease(ctx)
	if err != nil {
		if background {
			// The resolve failed before a target tag was known; key the
			// record to the cached tag that armed this spawn so the retry
			// policy can match it.
			failTarget := "unknown"
			if cached, ok := readVersionCheck(); ok && cached.Latest != "" {
				failTarget = cached.Latest
			}
			recordAutoUpdateFailure(failTarget, "resolve: "+err.Error(), false)
		}
		return render(stdout, g, output.Failure("UPGRADE_FAILED", "Could not resolve the latest release: "+err.Error(), "Check network access to GitHub releases.", nil), 1)
	}
	if !versionOutdated(version.Version, tag) {
		writeVersionCheck(tag)
		// Same partial-record rule as the hash-equal branch below: being
		// current vouches for the binary, not for the plugin refresh.
		if rec, ok := readAutoUpdateFailure(); !ok || !rec.Partial {
			clearAutoUpdateFailure()
		}
		return render(stdout, g, output.Success(map[string]any{"version": version.Version, "latest": tag}, "Already up to date.", nil), 0)
	}
	newBinary, cleanup, err := fetchReleaseBinary(ctx, assetURL, checksumsURL)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		if background {
			recordAutoUpdateFailure(tag, err.Error(), false)
		}
		return render(stdout, g, output.Failure("UPGRADE_FAILED", err.Error(), "Re-run baseloop upgrade, or reinstall: curl -fsSL https://app.baseloop.io/install-cli | bash", nil), 1)
	}
	// A delayed duplicate child runs OLD code, so its own version always
	// looks outdated; comparing the downloaded binary against what is already
	// on disk is what stops it from re-swapping an identical binary a winner
	// already installed (and from re-running setup on top of it).
	if newHash, hashErr := fileSHA256(newBinary); hashErr == nil {
		if curHash, curErr := fileSHA256(target); curErr == nil && curHash == newHash {
			writeVersionCheck(tag)
			// This branch skips setup, so it can only vouch for the binary:
			// a PARTIAL record (the winner's swap landed but its plugin
			// refresh failed) must survive, or the "run baseloop setup
			// skills" recovery notice disappears while the plugins are
			// still stale.
			if rec, ok := readAutoUpdateFailure(); !ok || !rec.Partial {
				clearAutoUpdateFailure()
			}
			return render(stdout, g, output.Success(map[string]any{"version": tag, "latest": tag}, "Already up to date.", nil), 0)
		}
	}
	// Lock staleness is wall-clock while our own deadlines are monotonic: a
	// machine that suspended mid-download can pass every check above while a
	// takeover already happened. Re-reading ownership immediately before the
	// swap is what keeps a usurped holder from racing the usurper's renames.
	if !lock.stillOwned() {
		if background {
			return render(stdout, g, output.Success(map[string]any{"skipped": "lock-taken-over"}, "Lock was taken over by a newer upgrade; exiting.", nil), 0)
		}
		return render(stdout, g, output.Failure("UPGRADE_IN_PROGRESS", "The upgrade lock was taken over by another process; not swapping.", "Re-run baseloop upgrade.", nil), 1)
	}
	if err := replaceBinary(newBinary, target); err != nil {
		if background && !errors.Is(err, os.ErrNotExist) {
			// ENOENT mid-swap is the uninstall race: write nothing.
			recordAutoUpdateFailure(tag, "swap: "+err.Error(), false)
		}
		return render(stdout, g, output.Failure("UPGRADE_FAILED", "Could not replace "+target+": "+err.Error(), "Check write permission on the install directory, or reinstall: curl -fsSL https://app.baseloop.io/install-cli | bash", nil), 1)
	}
	writeVersionCheck(tag)
	// Skills are embedded in the binary, so refreshing them must run the NEW
	// binary: this process still executes the old code. The child env keeps
	// BASELOOP_UPGRADE_CHILD=1 visible to this subprocess and its children —
	// load-bearing: `setup` is not notice-excluded, so the inherited marker is
	// the only thing preventing a grandchild spawn.
	notes, setupSkipped := runPostUpgradeSetup(target)
	if background && len(notes) > 0 && !setupSkipped {
		// Swap landed, plugin refresh did not: a partial record, whose
		// recovery is `baseloop setup skills` — not another upgrade.
		recordAutoUpdateFailure(tag, strings.Join(notes, "; "), true)
	} else {
		clearAutoUpdateFailure()
	}
	payload := map[string]any{"from": version.Version, "to": tag, "binary": target}
	if len(notes) > 0 {
		payload["notes"] = notes
	}
	return render(stdout, g, output.Success(payload, "Upgraded baseloop to "+tag+".", nil), 0)
}

// fileSHA256 returns the lowercase hex SHA-256 of the file at path.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// fetchReleaseBinary downloads the release archive, verifies it against the
// release's checksums.txt, extracts it, and returns the path of the contained
// binary. Verification is mandatory: a release without a fetchable checksum
// entry fails the upgrade rather than falling back to TLS-only trust.
func fetchReleaseBinary(ctx context.Context, assetURL, checksumsURL string) (string, func(), error) {
	if checksumsURL == "" {
		return "", nil, fmt.Errorf("release publishes no checksums.txt; refusing unverified binary upgrade")
	}
	checksums, err := httpGetBytes(ctx, checksumsURL)
	if err != nil {
		return "", nil, fmt.Errorf("could not fetch checksums.txt: %w", err)
	}
	assetName := gtmAssetBaseName(assetURL)
	expectedSHA := shaForFile(checksums, assetName)
	if expectedSHA == "" {
		return "", nil, fmt.Errorf("checksums.txt has no entry for %s; refusing unverified binary upgrade", assetName)
	}
	suffix := ".tar.gz"
	if strings.HasSuffix(strings.ToLower(assetName), ".zip") {
		suffix = ".zip"
	}
	archive, err := downloadToTemp(ctx, assetURL, "baseloop-upgrade-*"+suffix)
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.Remove(archive) }
	if err := verifyFileSHA(archive, expectedSHA); err != nil {
		return "", cleanup, err
	}
	extracted, err := os.MkdirTemp("", "baseloop-upgrade-bin-*")
	if err != nil {
		return "", cleanup, err
	}
	archiveCleanup := cleanup
	cleanup = func() {
		archiveCleanup()
		_ = os.RemoveAll(extracted)
	}
	if suffix == ".zip" {
		err = extractZipFile(archive, extracted)
	} else {
		err = extractTarGzFile(archive, extracted)
	}
	if err != nil {
		return "", cleanup, err
	}
	binary, err := findBinaryInDir(extracted)
	if err != nil {
		return "", cleanup, err
	}
	return binary, cleanup, nil
}

func downloadToTemp(ctx context.Context, url, pattern string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil) //nolint:gosec // URL comes from the resolved release.
	if err != nil {
		return "", err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", fmt.Errorf("download %s returned HTTP %d", url, res.StatusCode)
	}
	tmp, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", err
	}
	path := tmp.Name()
	if _, err := io.Copy(tmp, res.Body); err != nil {
		_ = tmp.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

// findBinaryInDir locates the baseloop binary inside an extracted release
// archive, wherever the archive nests it.
func findBinaryInDir(dir string) (string, error) {
	want := "baseloop"
	if runtime.GOOS == "windows" {
		want = "baseloop.exe"
	}
	var found string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if d.Name() == want {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", fmt.Errorf("release archive contains no %s binary", want)
	}
	return found, nil
}

// replaceBinary swaps target for the binary at src using same-directory
// renames: stage a copy next to target (rename across filesystems would fail
// from a temp dir), move the live binary aside, move the staged copy in. The
// aside-rename is what makes this Windows-safe — a running executable cannot
// be deleted or overwritten there, but it can be renamed. Removing the old
// copy is best-effort for the same reason; a leftover .old is cleared on the
// next upgrade.
func replaceBinary(src, target string) error {
	staged := target + ".new"
	if err := copyFileMode(src, staged, 0o755); err != nil {
		return err
	}
	backup := target + ".old"
	_ = os.Remove(backup)
	if err := os.Rename(target, backup); err != nil {
		_ = os.Remove(staged)
		return err
	}
	if err := os.Rename(staged, target); err != nil {
		if rollback := os.Rename(backup, target); rollback != nil {
			return fmt.Errorf("swap failed (%v) and rollback failed (%v); previous binary is at %s", err, rollback, backup)
		}
		_ = os.Remove(staged)
		return err
	}
	_ = os.Remove(backup)
	return nil
}

func copyFileMode(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return err
	}
	return out.Close()
}

// runPostUpgradeSetup refreshes the agent skills and plugins by exec-ing the
// freshly installed binary. Failures downgrade to notes: the binary upgrade
// already succeeded, and `baseloop setup skills` can be re-run by hand.
// skipped distinguishes a deliberate BASELOOP_SKIP_SETUP skip from a real
// failure so callers never have to re-read the env var to interpret the notes.
func runPostUpgradeSetup(binary string) (notes []string, skipped bool) {
	if os.Getenv("BASELOOP_SKIP_SETUP") == "1" {
		return []string{"Skipped plugin refresh (BASELOOP_SKIP_SETUP=1). Run baseloop setup skills when ready."}, true
	}
	ctx, cancel := context.WithTimeout(context.Background(), postUpgradeSetupTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "setup", "skills")
	out, err := cmd.CombinedOutput()
	if err != nil && ctx.Err() == context.DeadlineExceeded {
		err = fmt.Errorf("timed out after %v", postUpgradeSetupTimeout)
	}
	if err != nil {
		detail := strings.TrimSpace(string(out))
		if len(detail) > 300 {
			detail = detail[:300] + "…"
		}
		note := "baseloop setup skills failed after the upgrade: " + err.Error()
		if detail != "" {
			note += " (" + detail + ")"
		}
		notes = append(notes, note+". Re-run it manually.")
	}
	return notes, false
}
