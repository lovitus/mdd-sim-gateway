package main

import (
	"context"
	"errors"
	"flag"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressexec"
)

func runEgress(arguments []string) error {
	flags := flag.NewFlagSet("run-egress", flag.ContinueOnError)
	desired := flags.String("desired", "", "absolute country exit desired-state path")
	status := flags.String("status", "", "absolute runtime status path")
	stateDir := flags.String("state-dir", "", "absolute private runtime state directory")
	singBox := flags.String("sing-box", "", "absolute sing-box executable path")
	portBase := flags.Int("proxy-port-base", 22000, "loopback country proxy port base")
	poll := flags.Duration("poll", 2*time.Second, "desired-state polling interval")
	coreConfig := flags.String("core-config", "", "0600 Core configuration used only to locate local IPC")
	tokenPath := flags.String("cellular-token", "", "0600 cellular egress IPC token")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || strings.TrimSpace(*desired) == "" || strings.TrimSpace(*status) == "" ||
		strings.TrimSpace(*stateDir) == "" || strings.TrimSpace(*singBox) == "" ||
		strings.TrimSpace(*coreConfig) == "" || strings.TrimSpace(*tokenPath) == "" {
		return errors.New("-desired, -status, -state-dir, -sing-box, -core-config, and -cellular-token are required")
	}
	core, err := loadConfig(*coreConfig)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return egressexec.Run(ctx, egressexec.Settings{
		DesiredPath: *desired, StatusPath: *status, StateDir: *stateDir,
		SingBoxPath: *singBox, PortBase: *portBase, Poll: *poll,
		CoreURL: "http://" + core.Local.Listen, TokenPath: *tokenPath,
	})
}
