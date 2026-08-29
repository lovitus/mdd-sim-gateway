package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/http"
	"os/user"
	"strconv"
	"strings"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/providerapply"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providerconfig"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/providerdeploy"
)

type coreMaintenanceClient struct {
	baseURL string
	token   string
	client  *http.Client
}

func (client coreMaintenanceClient) Request(ctx context.Context, request providerapply.DrainRequest, begin bool) (providerapply.DrainResult, error) {
	return providerapply.RequestMaintenance(ctx, client.baseURL, client.token, request, begin, client.client)
}

func (client coreMaintenanceClient) Snapshot(ctx context.Context) (providerapply.Snapshot, error) {
	return providerapply.Fetch(ctx, client.baseURL+providerapply.Path, client.token, client.client)
}

func runProviderApply(arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("apply-provider-configs", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "path to the running mdd-core configuration")
	candidatePath := flags.String("candidate", "", "rendered candidate provider directory")
	currentLink := flags.String("current-link", "", "atomic current provider configuration link")
	receiptPath := flags.String("receipt-dir", "", "0700 provider apply receipt directory")
	providerBinary := flags.String("provider-binary", "", "installed root-owned Provider executable")
	providerUser := flags.String("provider-user", "mdd", "Provider service account")
	systemctlPath := flags.String("systemctl", "/bin/systemctl", "absolute systemctl executable")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if strings.TrimSpace(*configPath) == "" || strings.TrimSpace(*candidatePath) == "" ||
		strings.TrimSpace(*currentLink) == "" || strings.TrimSpace(*receiptPath) == "" ||
		strings.TrimSpace(*providerBinary) == "" || flags.NArg() != 0 {
		return errors.New("-config, -candidate, -current-link, -receipt-dir, and -provider-binary are required")
	}
	if err := requireProviderApplyPrivileges(); err != nil {
		return err
	}
	settings, err := loadConfig(*configPath)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	receipt, applyErr := executeProviderCandidate(ctx, settings, *candidatePath, *currentLink, *receiptPath, *providerBinary, *providerUser, *systemctlPath)
	var blocked providerApplyBlockedError
	if errors.As(applyErr, &blocked) {
		_ = json.NewEncoder(output).Encode(blocked.plan)
	}
	if receipt.SchemaVersion != 0 {
		if err := json.NewEncoder(output).Encode(receipt); err != nil && applyErr == nil {
			return err
		}
	}
	return applyErr
}

func executeProviderCandidate(ctx context.Context, settings config, candidatePath, currentLink, receiptPath, providerBinary, providerUser, systemctlPath string) (providerdeploy.Receipt, error) {
	account, err := user.Lookup(strings.TrimSpace(providerUser))
	if err != nil {
		return providerdeploy.Receipt{}, errors.New("provider service account was not found")
	}
	uid, uidErr := strconv.Atoi(account.Uid)
	gid, gidErr := strconv.Atoi(account.Gid)
	if uidErr != nil || gidErr != nil || uid < 1 || gid < 1 {
		return providerdeploy.Receipt{}, errors.New("provider service account has an invalid Unix identity")
	}
	candidate, err := providerconfig.LoadDirectory(candidatePath)
	if err != nil {
		return providerdeploy.Receipt{}, err
	}
	if err := providerdeploy.ValidateArtifacts(candidatePath, providerBinary, candidate, uid, gid); err != nil {
		return providerdeploy.Receipt{}, err
	}
	previousTarget, err := providerdeploy.CurrentTarget(currentLink)
	if err != nil {
		return providerdeploy.Receipt{}, err
	}
	var current providerconfig.Manifest
	if previousTarget != "" {
		current, err = providerconfig.LoadDirectory(previousTarget)
		if err != nil {
			return providerdeploy.Receipt{}, err
		}
	}
	manager := providerdeploy.Systemctl{Path: systemctlPath}
	if err := manager.Validate(); err != nil {
		return providerdeploy.Receipt{}, err
	}
	baseURL, err := providerCoreAddress(settings.Local.Listen)
	if err != nil {
		return providerdeploy.Receipt{}, err
	}
	httpClient := &http.Client{Transport: &http.Transport{Proxy: nil}}
	preflight, err := providerapply.Fetch(ctx, baseURL+providerapply.Path, settings.Local.Token, httpClient)
	if err != nil {
		return providerdeploy.Receipt{}, err
	}
	plan := providerapply.BuildPlan(current, candidate, preflight)
	if !plan.Safe {
		return providerdeploy.Receipt{}, providerApplyBlockedError{plan: plan}
	}
	return providerdeploy.Execute(ctx, providerdeploy.ApplyInput{
		CurrentLink: currentLink, PreviousTarget: previousTarget, CandidateTarget: candidatePath,
		ReceiptDirectory: receiptPath, Candidate: candidate, Current: current, Plan: plan, Preflight: preflight,
		Manager: manager, Maintenance: coreMaintenanceClient{baseURL: baseURL, token: settings.Local.Token, client: httpClient},
	})
}
