package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/releasebundle"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "mdd-release: %v\n", err)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	flags := flag.NewFlagSet("mdd-release", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	output := flags.String("output", "", "new absolute release directory")
	releaseID := flags.String("release-id", "", "stable lowercase release identifier")
	revision := flags.String("source-revision", "", "exact Git source revision")
	architecture := flags.String("architecture", "", "linux target architecture (amd64 or arm64)")
	core := flags.String("core", "", "Linux mdd-core executable")
	agent := flags.String("agent", "", "optional Linux mdd-agent executable")
	provider := flags.String("provider", "", "Linux mdd-vowifi executable")
	coreUnit := flags.String("core-unit", "", "mdd-core.service")
	providerUnit := flags.String("provider-unit", "", "mdd-vowifi@.service")
	applyUnit := flags.String("provider-apply-unit", "", "mdd-provider-apply.service")
	egressUnit := flags.String("egress-unit", "", "mdd-egress.service")
	providerSource := flags.String("provider-source", "", "complete corresponding Provider source archive")
	providerNotice := flags.String("provider-notice", "", "Provider AGPL license notice")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || empty(*output, *releaseID, *revision, *architecture, *core, *provider, *coreUnit, *providerUnit, *applyUnit, *egressUnit, *providerSource, *providerNotice) {
		return errors.New("all release identity, Core, Provider, unit, source, and notice flags are required")
	}
	coreVersion, err := releasebundle.InspectGoExecutable(*core, "linux", strings.TrimSpace(*architecture))
	if err != nil {
		return err
	}
	providerVersion, err := releasebundle.InspectGoExecutable(*provider, "linux", strings.TrimSpace(*architecture))
	if err != nil {
		return err
	}
	inputs := []releasebundle.Input{
		{Name: "mdd-core", Role: releasebundle.RoleCore, Mode: 0o755, SourcePath: *core, GoVersion: coreVersion},
		{Name: "mdd-vowifi", Role: releasebundle.RoleProvider, Mode: 0o755, SourcePath: *provider, GoVersion: providerVersion},
		{Name: "mdd-core.service", Role: releasebundle.RoleCoreUnit, Mode: 0o644, SourcePath: *coreUnit},
		{Name: "mdd-vowifi@.service", Role: releasebundle.RoleProviderUnit, Mode: 0o644, SourcePath: *providerUnit},
		{Name: "mdd-provider-apply.service", Role: releasebundle.RoleApplyUnit, Mode: 0o644, SourcePath: *applyUnit},
		{Name: "mdd-egress.service", Role: releasebundle.RoleEgressUnit, Mode: 0o644, SourcePath: *egressUnit},
		{Name: "mdd-vowifi-source.tar.gz", Role: releasebundle.RoleProviderSource, Mode: 0o644, SourcePath: *providerSource},
		{Name: "LICENSE-NOTICE.md", Role: releasebundle.RoleProviderNotice, Mode: 0o644, SourcePath: *providerNotice},
	}
	if strings.TrimSpace(*agent) != "" {
		agentVersion, err := releasebundle.InspectGoExecutable(*agent, "linux", strings.TrimSpace(*architecture))
		if err != nil {
			return err
		}
		inputs = append(inputs, releasebundle.Input{Name: "mdd-agent", Role: releasebundle.RoleAgent, Mode: 0o755, SourcePath: *agent, GoVersion: agentVersion})
	}
	manifest, err := releasebundle.CreateDirectory(*output, releasebundle.Manifest{
		ReleaseID: strings.TrimSpace(*releaseID), SourceRevision: strings.TrimSpace(*revision),
		OS: "linux", Architecture: strings.TrimSpace(*architecture),
	}, inputs)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{
		"status": "created", "release_id": manifest.ReleaseID, "artifacts": len(manifest.Artifacts), "output": *output,
	})
}

func empty(values ...string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return true
		}
	}
	return false
}
