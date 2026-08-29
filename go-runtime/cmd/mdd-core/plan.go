package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"strings"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/providerapply"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providerconfig"
)

var errProviderApplyBlocked = errors.New("provider apply plan is blocked")

type providerApplyBlockedError struct{ plan providerapply.Plan }

func (failure providerApplyBlockedError) Error() string { return errProviderApplyBlocked.Error() }
func (failure providerApplyBlockedError) Unwrap() error { return errProviderApplyBlocked }

func runProviderPlan(arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("plan-provider-apply", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "path to the running mdd-core configuration")
	candidatePath := flags.String("candidate", "", "rendered candidate provider directory")
	currentPath := flags.String("current", "", "current provider directory, if one exists")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if strings.TrimSpace(*configPath) == "" || strings.TrimSpace(*candidatePath) == "" || flags.NArg() != 0 {
		return errors.New("-config and -candidate are required")
	}
	settings, err := loadConfig(*configPath)
	if err != nil {
		return err
	}
	candidate, err := providerconfig.LoadDirectory(*candidatePath)
	if err != nil {
		return err
	}
	var current providerconfig.Manifest
	if strings.TrimSpace(*currentPath) != "" {
		current, err = providerconfig.LoadDirectory(*currentPath)
		if err != nil {
			return err
		}
	}
	coreAddress, err := providerCoreAddress(settings.Local.Listen)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	preflight, err := providerapply.Fetch(ctx, coreAddress+providerapply.Path, settings.Local.Token, nil)
	cancel()
	if err != nil {
		return err
	}
	plan := providerapply.BuildPlan(current, candidate, preflight)
	if err := json.NewEncoder(output).Encode(plan); err != nil {
		return err
	}
	if !plan.Safe {
		return errProviderApplyBlocked
	}
	return nil
}
