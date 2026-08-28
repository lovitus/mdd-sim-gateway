// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/boa-z/vowifi-go/runtimehost/identity"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/agentaka"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/service"
)

const maximumConfigBytes = 64 << 10

type config struct {
	LineID     string `json:"line_id"`
	ProviderID string `json:"provider_id"`
	DeviceID   string `json:"device_id"`
	TraceID    string `json:"trace_id"`
	IPC        struct {
		Listen             string `json:"listen"`
		Token              string `json:"token"`
		StatePath          string `json:"state_path"`
		OperationTimeoutMS int    `json:"operation_timeout_ms"`
		ShutdownTimeoutMS  int    `json:"shutdown_timeout_ms"`
	} `json:"ipc"`
	Agent struct {
		BrokerURL         string `json:"broker_url"`
		BrokerToken       string `json:"broker_token"`
		ID                string `json:"id"`
		ProcessGeneration string `json:"process_generation"`
		SessionGeneration string `json:"session_generation"`
		CardID            string `json:"card_id"`
		TimeoutMS         int    `json:"timeout_ms"`
	} `json:"agent"`
	SIM struct {
		IMSI string `json:"imsi"`
		MCC  string `json:"mcc"`
		MNC  string `json:"mnc"`
		IMEI string `json:"imei"`
		SMSC string `json:"smsc"`
	} `json:"sim"`
	Network struct {
		EPDGAddress    string   `json:"epdg_address"`
		PCSCF          []string `json:"pcscf"`
		IKETimeoutMS   int      `json:"ike_timeout_ms"`
		CloseTimeoutMS int      `json:"close_timeout_ms"`
		MTU            int      `json:"mtu"`
	} `json:"network"`
	IMS struct {
		IMPI             string `json:"impi"`
		IMPU             string `json:"impu"`
		Domain           string `json:"domain"`
		AKAAppPreference string `json:"aka_app_preference"`
		Network          string `json:"network"`
		Server           string `json:"server"`
		TimeoutMS        int    `json:"timeout_ms"`
		Expires          int    `json:"expires"`
	} `json:"ims"`
}

func main() {
	path := flag.String("config", "", "path to the 0600 JSON configuration file")
	flag.Parse()
	if *path == "" {
		log.Fatal("-config is required")
	}
	settings, err := loadConfig(*path)
	if err != nil {
		log.Fatal(err)
	}
	if err := run(settings); err != nil {
		log.Fatal(err)
	}
}

func run(settings config) error {
	generation, err := newGeneration()
	if err != nil {
		return err
	}
	factory, err := service.NewUpstreamFactory(settings.upstream())
	if err != nil {
		return err
	}
	operations, err := service.OpenBoltOperationStore(settings.IPC.StatePath)
	if err != nil {
		return fmt.Errorf("open VoWiFi operation store: %w", err)
	}
	defer operations.Close()
	backend, err := service.NewBackendWithStore(settings.LineID, settings.ProviderID, generation, factory, operations)
	if err != nil {
		return err
	}
	api, err := vowifiipc.NewAPI(backend, settings.IPC.Token, durationMS(settings.IPC.OperationTimeoutMS, 60*time.Second))
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", settings.IPC.Listen)
	if err != nil {
		return fmt.Errorf("listen on VoWiFi IPC endpoint: %w", err)
	}
	defer listener.Close()
	server := &http.Server{Handler: api, ReadHeaderTimeout: 5 * time.Second}
	serveErr := make(chan error, 1)
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	var serveFailure error
	select {
	case err := <-serveErr:
		serveFailure = err
	case <-signals:
	}
	shutdownTimeout := durationMS(settings.IPC.ShutdownTimeoutMS, 10*time.Second)
	shutdownContext, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	serverErr := server.Shutdown(shutdownContext)
	cancel()
	stopContext, cancelStop := context.WithTimeout(context.Background(), shutdownTimeout)
	stopResult, stopErr := backend.Stop(stopContext, vowifiipc.LifecycleRequest{OperationID: "shutdown-" + generation})
	cancelStop()
	_ = stopResult
	return errors.Join(serveFailure, serverErr, stopErr)
}

func loadConfig(path string) (config, error) {
	var settings config
	info, err := os.Stat(path)
	if err != nil {
		return settings, fmt.Errorf("stat VoWiFi config: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maximumConfigBytes {
		return settings, errors.New("VoWiFi config must be a non-empty regular file no larger than 64 KiB")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return settings, errors.New("VoWiFi config contains tokens and must not be accessible by group or others")
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return settings, fmt.Errorf("read VoWiFi config: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		return settings, fmt.Errorf("decode VoWiFi config: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return settings, errors.New("VoWiFi config has trailing JSON")
	}
	if err := settings.validate(); err != nil {
		return settings, err
	}
	return settings, nil
}

func (settings config) validate() error {
	if strings.TrimSpace(settings.LineID) == "" || strings.TrimSpace(settings.ProviderID) == "" ||
		strings.TrimSpace(settings.DeviceID) == "" || len(settings.IPC.Token) < 32 ||
		len(settings.Agent.BrokerToken) < 32 || strings.TrimSpace(settings.IPC.StatePath) == "" {
		return errors.New("VoWiFi config is missing runtime identity or token")
	}
	host, port, err := net.SplitHostPort(settings.IPC.Listen)
	if err != nil || port == "" {
		return errors.New("VoWiFi IPC listen address must include a port")
	}
	address, err := netip.ParseAddr(strings.Trim(host, "[]"))
	if err != nil || !address.IsLoopback() {
		return errors.New("VoWiFi IPC must listen on a literal loopback address")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return errors.New("VoWiFi IPC listen port must be between 1 and 65535")
	}
	if !filepath.IsAbs(settings.IPC.StatePath) {
		return errors.New("VoWiFi operation state path must be absolute")
	}
	for _, value := range []int{
		settings.IPC.OperationTimeoutMS, settings.IPC.ShutdownTimeoutMS, settings.Agent.TimeoutMS,
		settings.Network.IKETimeoutMS, settings.Network.CloseTimeoutMS, settings.IMS.TimeoutMS,
	} {
		if value < 0 || value > 120_000 {
			return errors.New("VoWiFi timeout must be between 0 and 120000 ms")
		}
	}
	return nil
}

func (settings config) upstream() service.UpstreamConfig {
	return service.UpstreamConfig{
		LineID: settings.LineID, DeviceID: settings.DeviceID, TraceID: settings.TraceID,
		Profile: identity.Profile{
			IMSI: settings.SIM.IMSI, MCC: settings.SIM.MCC, MNC: settings.SIM.MNC,
			IMEI: settings.SIM.IMEI, SMSC: settings.SIM.SMSC,
		},
		EPDGAddress: settings.Network.EPDGAddress, PCSCF: settings.Network.PCSCF,
		IMPI: settings.IMS.IMPI, IMPU: settings.IMS.IMPU, IMSDomain: settings.IMS.Domain,
		AKAAppPreference: settings.IMS.AKAAppPreference,
		Agent: agentaka.Config{
			AgentID: settings.Agent.ID, ProcessGeneration: settings.Agent.ProcessGeneration,
			SessionGeneration: settings.Agent.SessionGeneration, CardID: settings.Agent.CardID,
			Timeout: durationMS(settings.Agent.TimeoutMS, 15*time.Second),
		},
		BrokerURL: settings.Agent.BrokerURL, BrokerToken: settings.Agent.BrokerToken,
		IKETimeout:   durationMS(settings.Network.IKETimeoutMS, 30*time.Second),
		SIPTimeout:   durationMS(settings.IMS.TimeoutMS, 15*time.Second),
		CloseTimeout: durationMS(settings.Network.CloseTimeoutMS, 5*time.Second),
		MTU:          settings.Network.MTU, SIPNetwork: settings.IMS.Network, SIPServer: settings.IMS.Server,
		SIPExpires: settings.IMS.Expires,
	}
}

func durationMS(value int, fallback time.Duration) time.Duration {
	if value <= 0 {
		return fallback
	}
	return time.Duration(value) * time.Millisecond
}

func newGeneration() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate provider process generation: %w", err)
	}
	return "vowifi-" + hex.EncodeToString(value[:]), nil
}
