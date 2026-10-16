package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"
)

func interactiveTerminal(input io.Reader) bool {
	file, ok := input.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}

type setupTerminal struct {
	input  *os.File
	output io.Writer
}

func newSetupTerminal(input io.Reader, output io.Writer) (*setupTerminal, error) {
	if !interactiveTerminal(input) {
		return nil, errors.New("first-run setup needs a terminal; use config init/config set or supply an existing -config for unattended use")
	}
	file := input.(*os.File)
	return &setupTerminal{input: file, output: output}, nil
}

func (ui *setupTerminal) ask(label, fallback string, secret bool) (string, error) {
	if fallback != "" && !secret {
		fmt.Fprintf(ui.output, "%s [%s]: ", label, fallback)
	} else {
		fmt.Fprintf(ui.output, "%s: ", label)
	}
	var value string
	if secret {
		payload, err := term.ReadPassword(int(ui.input.Fd()))
		fmt.Fprintln(ui.output)
		if err != nil {
			return "", err
		}
		value = string(payload)
		clear(payload)
	} else {
		// Do not buffer ahead of ReadPassword, which reads the terminal FD.
		var line []byte
		for len(line) <= 4096 {
			var character [1]byte
			if _, err := io.ReadFull(ui.input, character[:]); err != nil {
				return "", err
			}
			if character[0] == '\n' {
				break
			}
			line = append(line, character[0])
		}
		if len(line) > 4096 {
			return "", errors.New("setup input is too long")
		}
		value = strings.TrimSpace(string(line))
	}
	if value == "" {
		value = fallback
	}
	return value, nil
}

func (ui *setupTerminal) choose(found []configCandidate) (string, error) {
	for index, item := range found {
		state := item.AgentID
		if item.Err != nil {
			state = item.Err.Error()
		}
		fmt.Fprintf(ui.output, "%d. %s (%s)\n", index+1, item.Path, state)
	}
	value, err := ui.ask("Configuration number", "", false)
	if err != nil {
		return "", err
	}
	index, err := strconv.Atoi(value)
	if err != nil || index < 1 || index > len(found) {
		return "", errors.New("invalid configuration selection; no files changed")
	}
	return found[index-1].Path, found[index-1].Err
}

func runSetupCLI(requested string, input io.Reader, output io.Writer) (config, string, error) {
	ui, err := newSetupTerminal(input, output)
	if err != nil {
		return config{}, "", err
	}
	path, err := resolveConfigPath(requested, ui.choose)
	if err != nil {
		return config{}, "", err
	}
	if _, pendingErr := os.Lstat(path + ".setup-pending"); errors.Is(pendingErr, os.ErrNotExist) {
		if settings, err := loadConfig(path); err == nil {
			fmt.Fprintf(output, "Configuration already complete: %s\nAgent: %s (runtime connection not checked)\n", path, settings.Agent.ID)
			return settings, path, nil
		}
	}
	tx, err := beginSetup(path)
	if err != nil {
		// Explicit paths never silently fall through to another identity.
		explicit, _ := explicitOrEnvironmentPath(requested)
		if explicit != "" {
			return config{}, path, err
		}
		if !errors.Is(err, os.ErrPermission) && !errors.Is(err, syscall.EROFS) && !errors.Is(err, syscall.ENOSPC) {
			return config{}, path, err
		}
		if _, pendingErr := os.Lstat(path + ".setup-pending"); !errors.Is(pendingErr, os.ErrNotExist) {
			return config{}, path, err
		}
		if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
			return config{}, path, err
		}
		paths, _ := configLocations()
		for _, next := range paths[1:] {
			candidate, nextErr := beginSetup(next)
			if nextErr == nil {
				path, tx, err = next, candidate, nil
				break
			}
		}
		if err != nil {
			fmt.Fprintf(output, "Default configuration/state paths are not writable: %v\n", err)
			custom, askErr := ui.ask("Absolute configuration file path", "", false)
			if askErr != nil {
				return config{}, path, askErr
			}
			path = custom
			tx, err = beginSetup(path)
		}
	}
	if err != nil {
		return config{}, path, err
	}
	defer tx.close()
	fmt.Fprintf(output, "Configuration: %s\nState: %s\nAgent: %s\n", path, filepath.Join(filepath.Dir(path), "state"), tx.pending.Settings.Agent.ID)
	confirmation, err := ui.ask("Continue this setup? (yes/no)", "no", false)
	if err != nil || confirmation != "yes" {
		return config{}, path, errors.New("setup cancelled; any pending enrollment is retained")
	}
	var form setupInput
	if !tx.pending.Requested && tx.pending.Settings.Agent.ServerToken == "" {
		method, askErr := ui.ask("1 Web login, 2 Connection package, 3 Existing Agent token", "1", false)
		if askErr != nil {
			return config{}, path, askErr
		}
		switch method {
		case "2":
			form.Package, err = ui.ask("Paste connection JSON (hidden)", "", true)
		case "3":
			form.AgentID, err = ui.ask("Agent ID", tx.pending.Settings.Agent.ID, false)
			if err == nil {
				form.Token, err = ui.ask("Agent token (hidden)", "", true)
			}
		case "1":
		default:
			return config{}, path, errors.New("invalid setup method")
		}
		if err != nil {
			return config{}, path, err
		}
	}
	if form.Package == "" {
		if !tx.pending.Requested {
			form.Server, err = ui.ask("Core HTTPS URL", tx.pending.Settings.Agent.ServerURL, false)
			if err != nil {
				return config{}, path, err
			}
			form.Fingerprint, err = ui.ask("Trusted leaf SHA-256 (blank = system CA; obtain self-signed pin from trusted Web Security page)", tx.pending.Settings.Agent.TLSFingerprint, false)
			if err != nil {
				return config{}, path, err
			}
		}
		if tx.pending.Settings.Agent.ServerToken == "" && form.Token == "" {
			form.Username, err = ui.ask("Web username", "admin", false)
			if err == nil {
				form.Password, err = ui.ask("Web password (not saved)", "", true)
			}
			if err != nil {
				return config{}, path, err
			}
		}
	}
	signals, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	ctx, cancel := context.WithTimeout(signals, 90*time.Second)
	defer cancel()
	settings, warning, err := tx.enroll(ctx, form)
	if err == nil && ctx.Err() != nil {
		err = fmt.Errorf("configuration saved, but this invocation was cancelled; Agent was not started: %w", ctx.Err())
	}
	form.Password, form.Package, form.Token = "", "", ""
	if warning != "" {
		fmt.Fprintln(output, warning)
	}
	if err != nil {
		return config{}, path, fmt.Errorf("setup: %w; retry config setup -config %q to resume, do not create another identity", err, path)
	}
	fmt.Fprintf(output, "Saved %s. TLS and Agent credential verified; hardware/runtime connection has not been started.\n", path)
	return settings, path, nil
}
