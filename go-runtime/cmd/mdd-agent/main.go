package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentcontrol"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agenthost"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentsim"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/pcscmonitor"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/pintls"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/recovery"
)

const maximumAgentConfigBytes = 64 << 10

type config struct {
	Version int `json:"version"`
	Agent   struct {
		ID             string            `json:"id"`
		ServerURL      string            `json:"server_url"`
		ServerToken    string            `json:"server_token"`
		TLSFingerprint string            `json:"tls_sha256"`
		PINs           map[string]string `json:"pins"`
		ModemEnabled   bool              `json:"modem_enabled"`
	} `json:"agent"`
	Control struct {
		Listen string `json:"listen"`
		Token  string `json:"token"`
	} `json:"control"`
	ScanIntervalMS          int `json:"scan_interval_ms"`
	RetryBaseMS             int `json:"retry_base_ms"`
	RetryCapMS              int `json:"retry_cap_ms"`
	OperationTimeoutSeconds int `json:"operation_timeout_seconds"`
}

func main() {
	if len(os.Args) < 2 {
		fatalf("usage: mdd-agent <run|status|start|stop> -config /absolute/path.json")
	}
	command := os.Args[1]
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	configPath := flags.String("config", os.Getenv("MDD_AGENT_CONFIG"), "path to the 0600 Agent JSON configuration")
	if err := flags.Parse(os.Args[2:]); err != nil {
		fatalf("parse command: %v", err)
	}
	settings, err := loadConfig(*configPath)
	if err != nil {
		fatalf("load config: %v", err)
	}
	if command == "run" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		worker, err := buildWorker(settings)
		if err == nil {
			err = runHost(ctx, settings, worker)
		}
		if err != nil {
			fatalf("run: %v", err)
		}
		return
	}
	if command != "status" && command != "start" && command != "stop" {
		fatalf("unknown command %q", command)
	}
	if err := runClient(command, settings, os.Stdout); err != nil {
		fatalf("%s: %v", command, err)
	}
}

func loadConfig(path string) (config, error) {
	var settings config
	path = strings.TrimSpace(path)
	if !filepath.IsAbs(path) {
		return settings, errors.New("configuration path must be absolute")
	}
	info, err := os.Stat(path)
	if err != nil {
		return settings, err
	}
	if !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > maximumAgentConfigBytes {
		return settings, errors.New("configuration must be a non-empty regular file no larger than 64 KiB")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		return settings, fmt.Errorf("configuration permissions must be 0600, got %04o", info.Mode().Perm())
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return settings, err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		return settings, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return settings, errors.New("configuration has trailing JSON")
	}
	if err := settings.validate(); err != nil {
		return settings, err
	}
	return settings, nil
}

func (settings *config) validate() error {
	if settings.Version != 1 {
		return errors.New("unsupported Agent configuration version")
	}
	settings.Agent.ID = strings.TrimSpace(settings.Agent.ID)
	settings.Agent.ServerURL = strings.TrimSpace(settings.Agent.ServerURL)
	settings.Agent.ServerToken = strings.TrimSpace(settings.Agent.ServerToken)
	settings.Agent.TLSFingerprint = strings.TrimSpace(settings.Agent.TLSFingerprint)
	settings.Control.Listen = strings.TrimSpace(settings.Control.Listen)
	settings.Control.Token = strings.TrimSpace(settings.Control.Token)
	if err := (agentlink.Hello{SchemaVersion: 1, AgentID: settings.Agent.ID, ProcessGeneration: "validation"}).Validate(); err != nil {
		return err
	}
	if len(settings.Agent.ServerToken) < 32 || len(settings.Control.Token) < 32 {
		return errors.New("Agent server and control tokens must each contain at least 32 bytes")
	}
	if settings.Agent.ModemEnabled {
		return errors.New("modem_enabled is not available in the PC/SC-only release")
	}
	if settings.ScanIntervalMS == 0 {
		settings.ScanIntervalMS = 1000
	}
	if settings.RetryBaseMS == 0 {
		settings.RetryBaseMS = 1000
	}
	if settings.RetryCapMS == 0 {
		settings.RetryCapMS = 30000
	}
	if settings.OperationTimeoutSeconds == 0 {
		settings.OperationTimeoutSeconds = 30
	}
	if settings.ScanIntervalMS < 100 || settings.ScanIntervalMS > 60000 || settings.RetryBaseMS < 100 ||
		settings.RetryCapMS < settings.RetryBaseMS || settings.RetryCapMS > 300000 ||
		settings.OperationTimeoutSeconds < 1 || settings.OperationTimeoutSeconds > 60 {
		return errors.New("Agent interval or timeout configuration is out of range")
	}
	for cardID, pin := range settings.Agent.PINs {
		if !digits(cardID, 1, 64) || pin != "" && !digits(pin, 4, 8) {
			return errors.New("PIN map must use numeric card IDs and 4-8 digit PINs")
		}
	}
	if _, err := pintls.NewHTTPClient(settings.Agent.ServerURL, settings.Agent.TLSFingerprint, 10*time.Second); err != nil {
		return err
	}
	if _, err := agentcontrol.NewClient("http://"+settings.Control.Listen, settings.Control.Token, nil); err != nil {
		return err
	}
	_, port, _ := net.SplitHostPort(settings.Control.Listen)
	parsedPort, portErr := strconv.ParseUint(port, 10, 16)
	if portErr != nil || parsedPort == 0 {
		return errors.New("Agent control address requires a fixed nonzero port")
	}
	return nil
}

func buildWorker(settings config) (*agenthost.Worker, error) {
	httpClient, err := pintls.NewHTTPClient(settings.Agent.ServerURL, settings.Agent.TLSFingerprint, 10*time.Second)
	if err != nil {
		return nil, err
	}
	return agenthost.New(agenthost.Config{
		ServerURL: settings.Agent.ServerURL, ServerToken: settings.Agent.ServerToken,
		AgentID: settings.Agent.ID, HTTPClient: httpClient,
		Monitors: pcscmonitor.Factory{}, Connector: agentsim.PCSCConnector{}, PINs: settings.Agent.PINs,
		ScanEvery: time.Duration(settings.ScanIntervalMS) * time.Millisecond,
		Recovery:  recovery.Policy{Base: time.Duration(settings.RetryBaseMS) * time.Millisecond, Cap: time.Duration(settings.RetryCapMS) * time.Millisecond},
	})
}

func runHost(ctx context.Context, settings config, worker agentcontrol.Worker) error {
	controller, err := agentcontrol.New(worker, nil)
	if err != nil {
		return err
	}
	timeout := time.Duration(settings.OperationTimeoutSeconds) * time.Second
	api, err := agentcontrol.NewAPI(controller, settings.Control.Token, timeout)
	if err != nil {
		return err
	}
	listener, err := agentcontrol.ListenLoopback(settings.Control.Listen)
	if err != nil {
		return err
	}
	defer listener.Close()
	server := &http.Server{Handler: api, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	startContext, cancelStart := context.WithTimeout(ctx, timeout)
	_, startErr := controller.Start(startContext)
	cancelStart()
	if startErr != nil && ctx.Err() == nil {
		log.Printf("mdd-agent runtime start: %v", startErr)
	}
	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-serveDone:
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
	}
	stopContext, cancelStop := context.WithTimeout(context.Background(), timeout)
	_, stopErr := controller.Stop(stopContext)
	cancelStop()
	shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	shutdownErr := server.Shutdown(shutdownContext)
	cancelShutdown()
	return errors.Join(serveErr, stopErr, shutdownErr)
}

func runClient(command string, settings config, output io.Writer) error {
	client, err := agentcontrol.NewClient("http://"+settings.Control.Listen, settings.Control.Token, &http.Client{Timeout: time.Duration(settings.OperationTimeoutSeconds) * time.Second})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(settings.OperationTimeoutSeconds)*time.Second)
	defer cancel()
	var snapshot agentcontrol.Snapshot
	switch command {
	case "status":
		snapshot, err = client.Status(ctx)
	case "start":
		snapshot, err = client.Start(ctx)
	case "stop":
		snapshot, err = client.Stop(ctx)
	}
	if encodeErr := json.NewEncoder(output).Encode(snapshot); err == nil {
		err = encodeErr
	}
	return err
}

func digits(value string, minimum, maximum int) bool {
	if len(value) < minimum || len(value) > maximum {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func fatalf(format string, arguments ...any) {
	log.Printf("mdd-agent: "+format, arguments...)
	os.Exit(2)
}
