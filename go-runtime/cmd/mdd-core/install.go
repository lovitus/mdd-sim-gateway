package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

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
