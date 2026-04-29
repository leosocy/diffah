package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"github.com/leosocy/diffah/internal/imageio"
	"github.com/leosocy/diffah/pkg/config"
	"github.com/leosocy/diffah/pkg/diff"
)

// probeTimeout bounds the network check so a black-holed registry
// never hangs the diagnostic tool. Hardcoded — no flag yet.
const probeTimeout = 15 * time.Second

type tmpdirCheck struct{}

func (tmpdirCheck) Name() string { return "tmpdir" }

// Run writes a 1 KiB probe file into os.TempDir() (which honours
// $TMPDIR) and removes it. Any error along the way -> Fail.
func (tmpdirCheck) Run(_ context.Context) CheckResult {
	dir := os.TempDir()
	f, err := os.CreateTemp(dir, "diffah-doctor-*.probe")
	if err != nil {
		return CheckResult{
			Status: statusFail,
			Detail: fmt.Sprintf("create probe in %s: %v", dir, err),
			Hint:   "ensure $TMPDIR is writable, or set TMPDIR to a writable directory",
		}
	}
	probePath := f.Name()
	defer os.Remove(probePath)
	if _, err := f.Write(make([]byte, 1024)); err != nil {
		_ = f.Close()
		return CheckResult{
			Status: statusFail,
			Detail: fmt.Sprintf("write probe to %s: %v", probePath, err),
			Hint:   "ensure $TMPDIR has free space",
		}
	}
	if err := f.Close(); err != nil {
		return CheckResult{
			Status: statusFail,
			Detail: fmt.Sprintf("close probe %s: %v", probePath, err),
			Hint:   "filesystem may be flaky; check dmesg",
		}
	}
	return CheckResult{Status: statusOK, Detail: dir}
}

type authfileCheck struct{}

func (authfileCheck) Name() string { return "authfile" }

// Run resolves the standard containers-image authfile lookup chain via
// imageio.ResolveAuthFile, then reads the file and verifies it parses
// as JSON containing an 'auths' map. No registry round-trip is made
// here — that's --probe's job.
func (authfileCheck) Run(_ context.Context) CheckResult {
	path := imageio.ResolveAuthFile()
	if path == "" {
		return CheckResult{
			Status: statusWarn,
			Detail: "no authfile found in lookup chain; anonymous pulls only",
		}
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return CheckResult{
			Status: statusFail,
			Detail: fmt.Sprintf("resolved: %s — read error: %v", path, err),
			Hint:   "ensure the file is readable by the current user",
		}
	}
	var parsed struct {
		Auths map[string]json.RawMessage `json:"auths"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return CheckResult{
			Status: statusFail,
			Detail: fmt.Sprintf("resolved: %s — JSON parse error: %v", path, err),
			Hint:   "fix the JSON file or unset $REGISTRY_AUTH_FILE",
		}
	}
	if parsed.Auths == nil {
		return CheckResult{
			Status: statusFail,
			Detail: fmt.Sprintf("resolved: %s — missing 'auths' map", path),
			Hint:   "regenerate via 'docker login' or 'podman login'",
		}
	}
	return CheckResult{
		Status: statusOK,
		Detail: fmt.Sprintf("resolved: %s (%d registries configured)", path, len(parsed.Auths)),
	}
}

// networkCheck round-trips a single GetManifest against the registry
// reference supplied via --probe, with a 15s hard cap. Errors are
// classified through diff.ClassifyRegistryErr so the Detail string
// uses the same vocabulary as the importer's error messages.
type networkCheck struct {
	probe       string
	buildSysCtx registryContextBuilder
}

func (networkCheck) Name() string { return "network" }

func (n networkCheck) Run(parentCtx context.Context) CheckResult {
	if n.probe == "" {
		return CheckResult{
			Status: statusOK,
			Detail: "--probe not supplied; check skipped",
		}
	}
	if n.buildSysCtx == nil {
		return CheckResult{
			Status: statusFail,
			Detail: "internal: registry context builder is nil",
			Hint:   "this is a bug; please file an issue",
		}
	}
	sysctx, _, _, err := n.buildSysCtx()
	if err != nil {
		return CheckResult{
			Status: statusFail,
			Detail: err.Error(),
			Hint:   "verify --authfile / --tls-verify / --cert-dir / --creds flag combination",
		}
	}
	ref, err := imageio.ParseReference(n.probe)
	if err != nil {
		return CheckResult{
			Status: statusFail,
			Detail: err.Error(),
			Hint:   "use 'docker://registry/name:tag' (or another supported transport)",
		}
	}
	ctx, cancel := context.WithTimeout(parentCtx, probeTimeout)
	defer cancel()
	src, err := ref.NewImageSource(ctx, sysctx)
	if err != nil {
		return classifyAndFailNetwork(err, n.probe)
	}
	defer src.Close()
	if _, _, err := src.GetManifest(ctx, nil); err != nil {
		return classifyAndFailNetwork(err, n.probe)
	}
	return CheckResult{
		Status: statusOK,
		Detail: fmt.Sprintf("manifest reachable: %s", n.probe),
	}
}

func classifyAndFailNetwork(err error, ref string) CheckResult {
	classified := diff.ClassifyRegistryErr(err, ref)
	return CheckResult{
		Status: statusFail,
		Detail: classified.Error(),
		Hint:   "check connectivity, credentials, or TLS configuration",
	}
}

// configCheck calls pkg/config.Validate against the resolved
// DefaultPath. A missing file is OK (defaults are used); a present
// file that fails to parse is Fail. Doctor must be exempted from the
// persistent config-load hook in cmd/root.go for this check to fire
// when the file is malformed (see isExemptFromConfigLoad).
type configCheck struct{}

func (configCheck) Name() string { return "config" }

func (configCheck) Run(_ context.Context) CheckResult {
	path := config.DefaultPath()
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return CheckResult{
			Status: statusOK,
			Detail: "no config file (defaults in use)",
		}
	} else if err != nil {
		return CheckResult{
			Status: statusFail,
			Detail: fmt.Sprintf("%s: %v", path, err),
			Hint:   "ensure the config file is readable by the current user",
		}
	}
	if err := config.Validate(path); err != nil {
		return CheckResult{
			Status: statusFail,
			Detail: err.Error(),
			Hint:   "run 'diffah config validate' for the same diagnostic; or unset $DIFFAH_CONFIG",
		}
	}
	return CheckResult{
		Status: statusOK,
		Detail: fmt.Sprintf("loaded ok: %s", path),
	}
}
