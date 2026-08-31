package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/providerconfig"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providerdeploy"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/releaseinstall"
)

func runReleaseInstall(arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("install-release", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	source := flags.String("source", "", "verified absolute release directory")
	serviceUser := flags.String("service-user", "mdd", "system service account")
	useradd := flags.String("useradd", "/usr/sbin/useradd", "absolute useradd executable")
	groupadd := flags.String("groupadd", "/usr/sbin/groupadd", "absolute groupadd executable")
	nologin := flags.String("nologin", "/usr/sbin/nologin", "absolute nologin shell")
	systemctl := flags.String("systemctl", "/bin/systemctl", "absolute systemctl executable")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || strings.TrimSpace(*source) == "" || strings.TrimSpace(*serviceUser) != "mdd" {
		return errors.New("-source is required and the service account must be mdd")
	}
	if err := requireProviderApplyPrivileges(); err != nil || runtime.GOOS != "linux" {
		return errors.New("release installation requires root on Linux")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	layout := releaseinstall.DefaultLayout()
	if _, err := releaseinstall.Preflight(filepath.Clean(*source), layout); err != nil {
		return err
	}
	uid, gid, err := ensureServiceAccount(ctx, *useradd, *groupadd, *nologin)
	if err != nil {
		return err
	}
	manager := providerdeploy.Systemctl{Path: *systemctl}
	if err := manager.Validate(); err != nil {
		return err
	}
	receipt, installErr := releaseinstall.Activate(ctx, filepath.Clean(*source), layout, releaseinstall.Identity{
		RootUID: 0, RootGID: 0, ServiceUID: uid, ServiceGID: gid,
	}, systemdReloader{ctx: ctx, manager: manager})
	if receipt.SchemaVersion != 0 {
		if err := json.NewEncoder(output).Encode(receipt); err != nil && installErr == nil {
			return err
		}
	}
	return installErr
}

func runReleaseRecovery(arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("recover-release-install", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	systemctl := flags.String("systemctl", "/bin/systemctl", "absolute systemctl executable")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 {
		return errors.New("recover-release-install accepts only -systemctl")
	}
	if err := requireProviderApplyPrivileges(); err != nil || runtime.GOOS != "linux" {
		return errors.New("release recovery requires root on Linux")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	manager := providerdeploy.Systemctl{Path: *systemctl}
	if err := manager.Validate(); err != nil {
		return err
	}
	receipt, recoverErr := releaseinstall.Recover(ctx, releaseinstall.DefaultLayout(), 0, systemdReloader{ctx: ctx, manager: manager})
	if receipt.SchemaVersion != 0 {
		if err := json.NewEncoder(output).Encode(receipt); err != nil && recoverErr == nil {
			return err
		}
	}
	return recoverErr
}

func runReleaseUninstall(arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("uninstall-release", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	checkOnly := flags.Bool("check-only", false, "validate the managed release without changing it")
	systemctl := flags.String("systemctl", "/bin/systemctl", "absolute systemctl executable")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 {
		return errors.New("uninstall-release accepts only -check-only and -systemctl")
	}
	if err := requireProviderApplyPrivileges(); err != nil || runtime.GOOS != "linux" {
		return errors.New("release removal requires root on Linux")
	}
	if _, err := os.Lstat("/var/lib/mdd-agent/cellular-guard-enabled"); err == nil {
		return errors.New("release removal is blocked because persistent cellular isolation has been activated")
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("persistent cellular isolation state is unverifiable")
	}
	layout := releaseinstall.DefaultLayout()
	if *checkOnly {
		plan, err := releaseinstall.PreflightRemove(layout, 0, 0)
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(plan)
	}
	manager := providerdeploy.Systemctl{Path: *systemctl}
	if err := manager.Validate(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	gate := systemdRemovalGate{
		systemctl:       filepath.Clean(*systemctl),
		providerCurrent: filepath.Join(layout.ConfigDirectory, "providers-current"),
	}
	plan, err := releaseinstall.Remove(ctx, layout, 0, 0, gate, systemdReloader{ctx: ctx, manager: manager})
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(plan)
}

var providerRemovalUnit = regexp.MustCompile(`^mdd-vowifi@line-[0-9a-f]{32}\.service$`)

type systemdRemovalGate struct {
	systemctl       string
	providerCurrent string
}

func (gate systemdRemovalGate) VerifyInactiveDisabled(ctx context.Context) error {
	units := map[string]struct{}{
		"mdd-core.service":           {},
		"mdd-provider-apply.service": {},
		"mdd-egress.service":         {},
		"mdd-agent.service":          {},
		"mdd-cellular-guard.service": {},
	}
	configured, err := configuredProviderUnits(gate.providerCurrent)
	if err != nil {
		return err
	}
	for _, unit := range configured {
		units[unit] = struct{}{}
	}
	for _, arguments := range [][]string{
		{"list-units", "--all", "--type=service", "--plain", "--full", "--no-legend", "--no-pager", "mdd-vowifi@*.service"},
		{"list-unit-files", "--type=service", "--full", "--no-legend", "--no-pager", "mdd-vowifi@*.service"},
	} {
		output, exitCode, err := runSystemctl(ctx, gate.systemctl, arguments...)
		// systemctl list-unit-files returns 1 when a pattern has no matches.
		// That is the expected post-disable state; output still passes through
		// the strict managed-unit parser below, so diagnostic/error text cannot
		// masquerade as an empty inventory.
		if err != nil || (exitCode != 0 && exitCode != 1) {
			return errors.New("could not enumerate MDD provider units")
		}
		listed, err := parseProviderUnits(output)
		if err != nil {
			return err
		}
		for _, unit := range listed {
			units[unit] = struct{}{}
		}
	}
	ordered := make([]string, 0, len(units))
	for unit := range units {
		ordered = append(ordered, unit)
	}
	sort.Strings(ordered)
	for _, unit := range ordered {
		_, activeExit, err := runSystemctl(ctx, gate.systemctl, "is-active", "--quiet", unit)
		if err != nil || (activeExit != 3 && activeExit != 4) {
			return errors.New("an MDD service is active or its state is unverifiable")
		}
		enabledOutput, _, err := runSystemctl(ctx, gate.systemctl, "is-enabled", unit)
		if err != nil || !nonEnabledUnitState(enabledOutput) {
			return errors.New("an MDD service is enabled or its enablement is unverifiable")
		}
	}
	return nil
}

func configuredProviderUnits(currentLink string) ([]string, error) {
	providerTarget, err := providerdeploy.CurrentTarget(currentLink)
	if err != nil {
		return nil, errors.New("current provider manifest is unverifiable")
	}
	if providerTarget == "" {
		return nil, nil
	}
	manifest, err := providerconfig.LoadDirectory(providerTarget)
	if err != nil {
		return nil, errors.New("current provider manifest is unverifiable")
	}
	units := make([]string, 0, len(manifest.Providers))
	for _, entry := range manifest.Providers {
		unit := "mdd-vowifi@" + entry.UnitInstance + ".service"
		if !providerRemovalUnit.MatchString(unit) {
			return nil, errors.New("current provider manifest contains an invalid unit")
		}
		units = append(units, unit)
	}
	sort.Strings(units)
	return units, nil
}

func parseProviderUnits(output string) ([]string, error) {
	seen := map[string]struct{}{}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		unit := fields[0]
		if unit == "mdd-vowifi@.service" {
			continue
		}
		if !providerRemovalUnit.MatchString(unit) {
			return nil, errors.New("systemd returned an invalid MDD provider unit")
		}
		seen[unit] = struct{}{}
	}
	result := make([]string, 0, len(seen))
	for unit := range seen {
		result = append(result, unit)
	}
	sort.Strings(result)
	return result, nil
}

func nonEnabledUnitState(output string) bool {
	fields := strings.Fields(output)
	if len(fields) != 1 {
		return false
	}
	switch fields[0] {
	case "disabled", "static", "indirect", "linked", "linked-runtime", "masked", "masked-runtime", "not-found", "generated", "transient":
		return true
	default:
		return false
	}
}

func runSystemctl(ctx context.Context, path string, arguments ...string) (string, int, error) {
	command := exec.CommandContext(ctx, path, arguments...)
	command.Env = []string{"LC_ALL=C", "SYSTEMD_COLORS=0"}
	var output boundedInstallOutput
	command.Stdout, command.Stderr = &output, &output
	err := command.Run()
	if err == nil {
		return strings.TrimSpace(output.String()), 0, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return strings.TrimSpace(output.String()), exit.ExitCode(), nil
	}
	return strings.TrimSpace(output.String()), -1, err
}

type boundedInstallOutput struct{ bytes.Buffer }

func (output *boundedInstallOutput) Write(value []byte) (int, error) {
	original := len(value)
	if remaining := (64 << 10) - output.Len(); remaining > 0 {
		if len(value) > remaining {
			value = value[:remaining]
		}
		_, _ = output.Buffer.Write(value)
	}
	return original, nil
}

type systemdReloader struct {
	ctx     context.Context
	manager providerdeploy.Systemctl
}

func (reloader systemdReloader) DaemonReload() error {
	return reloader.manager.DaemonReload(reloader.ctx)
}

func ensureServiceAccount(ctx context.Context, useraddPath, groupaddPath, nologinPath string) (int, int, error) {
	if runtime.GOOS != "linux" {
		return 0, 0, errors.New("service account creation is supported only on Linux")
	}
	if account, err := user.Lookup("mdd"); err == nil {
		return numericIdentity(account)
	}
	for _, path := range []string{useraddPath, groupaddPath, nologinPath} {
		if err := validateRootExecutable(path); err != nil {
			return 0, 0, err
		}
	}
	if _, err := user.LookupGroup("mdd"); err != nil {
		if err := runAccountTool(ctx, groupaddPath, "--system", "mdd"); err != nil {
			if _, lookupErr := user.LookupGroup("mdd"); lookupErr != nil {
				return 0, 0, err
			}
		}
	}
	if err := runAccountTool(ctx, useraddPath, "--system", "--gid", "mdd", "--home-dir", "/var/lib/mdd",
		"--no-create-home", "--shell", nologinPath, "--comment", "MDD services", "mdd"); err != nil {
		if _, lookupErr := user.Lookup("mdd"); lookupErr != nil {
			return 0, 0, err
		}
	}
	account, err := user.Lookup("mdd")
	if err != nil {
		return 0, 0, errors.New("mdd service account was not created")
	}
	return numericIdentity(account)
}

func numericIdentity(account *user.User) (int, int, error) {
	uid, uidErr := strconv.Atoi(account.Uid)
	gid, gidErr := strconv.Atoi(account.Gid)
	if uidErr != nil || gidErr != nil || uid < 1 || gid < 1 || account.Username != "mdd" {
		return 0, 0, errors.New("mdd service account identity is invalid")
	}
	return uid, gid, nil
}

func validateRootExecutable(path string) error {
	path = filepath.Clean(strings.TrimSpace(path))
	if !filepath.IsAbs(path) || path == string(filepath.Separator) {
		return errors.New("account tool path must be absolute and scoped")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o111 == 0 || info.Mode().Perm()&0o022 != 0 {
		return errors.New("account tool must be a non-writable executable regular file")
	}
	return validateRootOwner(info)
}

func runAccountTool(ctx context.Context, path string, arguments ...string) error {
	command := exec.CommandContext(ctx, path, arguments...)
	command.Env = []string{"LC_ALL=C"}
	command.Stdout, command.Stderr = io.Discard, io.Discard
	if err := command.Run(); err != nil {
		return errors.New("service account tool failed")
	}
	return nil
}
