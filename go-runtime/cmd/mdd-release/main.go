package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
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
	agent := flags.String("agent", "", "Linux mdd-agent executable")
	agentAudio := flags.String("agent-audio-helper", "", "Linux mdd-call-audio-helper executable")
	updater := flags.String("updater", "", "detached Linux mdd-updater executable (optional)")
	updaterUnit := flags.String("updater-unit", "", "mdd-updater.service (optional)")
	updaterPath := flags.String("updater-path", "", "mdd-updater.path (optional)")
	provider := flags.String("provider", "", "Linux mdd-vowifi executable")
	coreUnit := flags.String("core-unit", "", "mdd-core.service")
	agentUnit := flags.String("agent-unit", "", "mdd-agent.service")
	guardUnit := flags.String("cellular-guard-unit", "", "mdd-cellular-guard.service")
	providerUnit := flags.String("provider-unit", "", "mdd-vowifi@.service")
	applyUnit := flags.String("provider-apply-unit", "", "mdd-provider-apply.service")
	egressUnit := flags.String("egress-unit", "", "mdd-egress.service")
	providerSource := flags.String("provider-source", "", "complete corresponding Provider source archive")
	providerNotice := flags.String("provider-notice", "", "Provider AGPL license notice")
	projectLicense := flags.String("project-license", "", "MDD GPL-3.0-only license text")
	projectNotice := flags.String("project-notice", "", "MDD project notice")
	thirdParty := flags.String("third-party-notices", "", "MDD third-party notices")
	goLicenses := flags.String("go-dependency-licenses", "", "collected Go dependency licenses and required source")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || empty(*output, *releaseID, *revision, *architecture, *core, *agent, *agentAudio, *provider, *coreUnit, *agentUnit, *guardUnit, *providerUnit, *applyUnit, *egressUnit, *providerSource, *providerNotice, *projectLicense, *projectNotice, *thirdParty, *goLicenses) {
		return errors.New("all release identity, Core, Agent, Provider, unit, source, and notice flags are required")
	}
	if strings.TrimSpace(*updater) != "" && !filepath.IsAbs(strings.TrimSpace(*updater)) {
		return errors.New("-updater must be an absolute path")
	}
	if (strings.TrimSpace(*updaterUnit) == "") != (strings.TrimSpace(*updaterPath) == "") || strings.TrimSpace(*updater) == "" && (strings.TrimSpace(*updaterUnit) != "" || strings.TrimSpace(*updaterPath) != "") {
		return errors.New("updater, updater-unit and updater-path must be supplied together")
	}
	coreVersion, err := releasebundle.InspectGoExecutable(*core, "linux", strings.TrimSpace(*architecture))
	if err != nil {
		return err
	}
	providerVersion, err := releasebundle.InspectGoExecutable(*provider, "linux", strings.TrimSpace(*architecture))
	if err != nil {
		return err
	}
	agentVersion, err := releasebundle.InspectGoExecutable(*agent, "linux", strings.TrimSpace(*architecture))
	if err != nil {
		return err
	}
	agentAudioVersion, err := releasebundle.InspectGoExecutable(*agentAudio, "linux", strings.TrimSpace(*architecture))
	if err != nil {
		return err
	}
	inputs := []releasebundle.Input{
		{Name: "mdd-core", Role: releasebundle.RoleCore, Mode: 0o755, SourcePath: *core, GoVersion: coreVersion},
		{Name: "mdd-agent", Role: releasebundle.RoleAgent, Mode: 0o755, SourcePath: *agent, GoVersion: agentVersion},
		{Name: "mdd-call-audio-helper", Role: releasebundle.RoleAgentAudio, Mode: 0o755, SourcePath: *agentAudio, GoVersion: agentAudioVersion},
		{Name: "mdd-vowifi", Role: releasebundle.RoleProvider, Mode: 0o755, SourcePath: *provider, GoVersion: providerVersion},
		{Name: "mdd-core.service", Role: releasebundle.RoleCoreUnit, Mode: 0o644, SourcePath: *coreUnit},
		{Name: "mdd-agent.service", Role: releasebundle.RoleAgentUnit, Mode: 0o644, SourcePath: *agentUnit},
		{Name: "mdd-cellular-guard.service", Role: releasebundle.RoleGuardUnit, Mode: 0o644, SourcePath: *guardUnit},
		{Name: "mdd-vowifi@.service", Role: releasebundle.RoleProviderUnit, Mode: 0o644, SourcePath: *providerUnit},
		{Name: "mdd-provider-apply.service", Role: releasebundle.RoleApplyUnit, Mode: 0o644, SourcePath: *applyUnit},
		{Name: "mdd-egress.service", Role: releasebundle.RoleEgressUnit, Mode: 0o644, SourcePath: *egressUnit},
		{Name: "mdd-vowifi-source.tar.gz", Role: releasebundle.RoleProviderSource, Mode: 0o644, SourcePath: *providerSource},
		{Name: "LICENSE-NOTICE.md", Role: releasebundle.RoleProviderNotice, Mode: 0o644, SourcePath: *providerNotice},
		{Name: "LICENSE", Role: releasebundle.RoleProjectLicense, Mode: 0o644, SourcePath: *projectLicense},
		{Name: "NOTICE", Role: releasebundle.RoleProjectNotice, Mode: 0o644, SourcePath: *projectNotice},
		{Name: "THIRD_PARTY_LICENSES.md", Role: releasebundle.RoleThirdParty, Mode: 0o644, SourcePath: *thirdParty},
		{Name: "go-dependency-licenses.tar.gz", Role: releasebundle.RoleGoLicenses, Mode: 0o644, SourcePath: *goLicenses},
	}
	if strings.TrimSpace(*updater) != "" {
		inputs = append(inputs, releasebundle.Input{Name: "mdd-updater", Role: releasebundle.RoleUpdater, Mode: 0o755, SourcePath: *updater, GoVersion: agentVersion})
		inputs = append(inputs, releasebundle.Input{Name: "mdd-updater.service", Role: releasebundle.RoleUpdaterUnit, Mode: 0o644, SourcePath: *updaterUnit})
		inputs = append(inputs, releasebundle.Input{Name: "mdd-updater.path", Role: releasebundle.RoleUpdaterPath, Mode: 0o644, SourcePath: *updaterPath})
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
