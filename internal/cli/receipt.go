// Install receipt: the policy an install was created under.
//
// The installers record it through the hidden `baseloop setup receipt`
// command right after the binary lands: "pinned" when the operator chose a
// version explicitly (BASELOOP_VERSION), "managed" otherwise. The update
// pipeline consumes it so a deliberately pinned machine is never nagged onto
// — or auto-updated away from — the version its operator chose. A manual
// `baseloop upgrade` is explicit consent to leave the pin, so a successful
// one rewrites the policy to managed (see upgrade.go).
package cli

import (
	"flag"
	"io"

	"github.com/baseloop-hq/baseloop-cli/internal/output"
	"github.com/baseloop-hq/baseloop-cli/internal/state"
)

const (
	installPolicyPinned  = "pinned"
	installPolicyManaged = "managed"
)

// installPolicy returns the recorded install policy, or "" when no receipt
// exists (installs that predate it, go-install builds) or the value is
// unknown. Unknown reads as unrecorded rather than failing: the receipt is
// advisory metadata, and a newer schema must not confuse an older binary.
func installPolicy() string {
	m, err := state.Load()
	if err != nil {
		return ""
	}
	switch m.InstallPolicy {
	case installPolicyPinned, installPolicyManaged:
		return m.InstallPolicy
	}
	return ""
}

// recordInstallPolicy load-modify-saves the manifest so fields owned by other
// writers (install.ps1's windows_user_path_entries) survive. A corrupt
// manifest starts over empty rather than making the receipt unwritable.
func recordInstallPolicy(policy string) error {
	m, err := state.Load()
	if err != nil {
		m = state.Manifest{}
	}
	m.InstallPolicy = policy
	return state.Save(m)
}

// setupReceipt is the hidden `setup receipt --policy <pinned|managed>`
// subcommand the installers call after placing the binary. Hidden because it
// records installer-owned metadata: it stays out of the catalog and usage
// text, like `upgrade --background`.
func setupReceipt(args []string, g globals, stdout io.Writer) int {
	fs := flag.NewFlagSet("setup receipt", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	policy := fs.String("policy", "", "Install policy: pinned or managed")
	if err := fs.Parse(args); err != nil {
		return render(stdout, g, output.Failure("USAGE", err.Error(), "Use baseloop setup receipt --policy <pinned|managed>.", nil), 2)
	}
	switch *policy {
	case installPolicyPinned, installPolicyManaged:
	default:
		return render(stdout, g, output.Failure("USAGE", "install policy must be pinned or managed", "Use baseloop setup receipt --policy <pinned|managed>.", nil), 2)
	}
	if err := recordInstallPolicy(*policy); err != nil {
		return render(stdout, g, output.Failure("STATE_ERROR", "Could not write the install receipt: "+err.Error(), "Check that the state directory is writable.", nil), 1)
	}
	manifestPath, _ := state.Path()
	return render(stdout, g, output.Success(map[string]any{"installPolicy": *policy, "manifest": manifestPath}, "Recorded install policy: "+*policy+".", nil), 0)
}
