package agentlink

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDeviceDefaultsEnvelopeRejectsMixedPayloadAndInvalidAuthority(t *testing.T) {
	value := DeviceDefaults{Authority: "core-fixture", Revision: 1}
	valid := envelope{Kind: kindDeviceDefaults, DeviceDefaults: &deviceDefaultsUpdate{Template: &value}}
	if err := valid.validate(); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []envelope{
		{Kind: kindDeviceDefaults},
		{Kind: kindHelloAck, DeviceDefaults: valid.DeviceDefaults},
		{Kind: kindDeviceDefaults, RequestID: "request", DeviceDefaults: valid.DeviceDefaults},
		{Kind: kindDeviceDefaults, Hello: &Hello{}, DeviceDefaults: valid.DeviceDefaults},
		{Kind: kindDeviceDefaults, PolicyRequest: &ModemPolicyRequest{}, DeviceDefaults: valid.DeviceDefaults},
		{Kind: kindDeviceDefaults, DeviceDefaults: &deviceDefaultsUpdate{Template: &DeviceDefaults{Revision: 1}}},
	} {
		if err := invalid.validate(); err == nil {
			t.Fatal("mixed or invalid default intent accepted")
		}
	}
	if err := (envelope{Kind: kindDeviceDefaults, DeviceDefaults: &deviceDefaultsUpdate{}}).validate(); err != nil {
		t.Fatal("clear rejected", err)
	}
}

func TestDeviceDefaultsNegotiateSyncChangesAndClearOnSourceFailure(t *testing.T) {
	server, err := NewServer(TokenResolverFunc(func(context.Context, string) (string, error) { return testToken, nil }))
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	value := DeviceDefaults{Authority: "core-fixture", Revision: 1, VoWiFiEnabled: true}
	available := true
	if err := server.SetDeviceDefaultsSource(func() (*DeviceDefaults, error) {
		mu.Lock()
		defer mu.Unlock()
		if !available {
			return nil, errors.New("fixture store unavailable")
		}
		copy := value
		return &copy, nil
	}); err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	updates := make(chan *DeviceDefaults, 16)
	done := make(chan error, 1)
	go func() {
		done <- (Client{URL: strings.Replace(httpServer.URL, "http://", "ws://", 1) + "/", Token: testToken,
			Hello:         Hello{SchemaVersion: SchemaVersion, AgentID: "defaults-agent", ProcessGeneration: "process-1"},
			Authenticator: &fakeAuthenticator{}, OperationTimeout: time.Second, HealthEvery: time.Second,
			Health: func() TopologySnapshot {
				return TopologySnapshot{ReaderCondition: ReaderReady, Readers: []ReaderFact{}}
			},
			DeviceDefaults: func(value *DeviceDefaults) error {
				if value != nil {
					copy := *value
					value = &copy
				}
				updates <- value
				return nil
			},
		}).Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("Agent did not exit")
		}
	})
	next := func() *DeviceDefaults {
		t.Helper()
		select {
		case update := <-updates:
			return update
		case err := <-done:
			done <- err
			t.Fatalf("Agent exited before defaults update: %v", err)
			return nil
		case <-time.After(4 * time.Second):
			t.Fatal("defaults update missing")
			return nil
		}
	}
	if next() != nil {
		t.Fatal("old session template not cleared before connecting")
	}
	if update := next(); update == nil || update.Revision != 1 || !update.VoWiFiEnabled {
		t.Fatal("initial authenticated template missing")
	}
	mu.Lock()
	value.Revision = 2
	value.ConnectionEnabled = true
	mu.Unlock()
	if update := next(); update == nil || update.Revision != 2 || !update.ConnectionEnabled {
		t.Fatal("health-driven update missing")
	}
	mu.Lock()
	available = false
	mu.Unlock()
	if next() != nil {
		t.Fatal("failed source retained active template")
	}
}

func TestDeviceDefaultsNeverSentWithoutCapability(t *testing.T) {
	server, err := NewServer(TokenResolverFunc(func(context.Context, string) (string, error) { return testToken, nil }))
	if err != nil {
		t.Fatal(err)
	}
	called := false
	if err := server.SetDeviceDefaultsSource(func() (*DeviceDefaults, error) { called = true; return nil, nil }); err != nil {
		t.Fatal(err)
	}
	if err := server.syncDeviceDefaults(context.Background(), &serverConnection{}); err != nil || called {
		t.Fatal("legacy Agent received defaults")
	}
}

func TestEnrollmentFactValidationAndIndependentCopies(t *testing.T) {
	value := DeviceEnrollment{FirstSeen: time.Unix(1000, 0), Initialized: true}
	if value.Validate() == nil {
		t.Fatal("initialized enrollment without default intent accepted")
	}
	value.Template = &DeviceDefaults{Authority: "fixture", Revision: 1, VoWiFiEnabled: true}
	if err := value.Validate(); err != nil {
		t.Fatal(err)
	}
	copy := value.Clone()
	copy.Template.VoWiFiEnabled = false
	if !value.Template.VoWiFiEnabled {
		t.Fatal("enrollment clone aliases source intent")
	}
}

func TestDefaultAuthorizationBindsAllIntentFieldsAndAuthority(t *testing.T) {
	key := []byte(strings.Repeat("k", 32))
	signed, err := (DeviceDefaults{Authority: "fixture", Revision: 4, VoWiFiEnabled: true}).Authorize(key)
	if err != nil || !signed.AuthorizedBy(key) {
		t.Fatal("signed template was not accepted", err)
	}
	for _, mutate := range []func(*DeviceDefaults){
		func(v *DeviceDefaults) { v.Authority = "other" }, func(v *DeviceDefaults) { v.Revision++ },
		func(v *DeviceDefaults) { v.ConnectionEnabled = true }, func(v *DeviceDefaults) { v.VoWiFiEnabled = false },
		func(v *DeviceDefaults) { v.FlightMode = true }, func(v *DeviceDefaults) { v.RoamingEnabled = true },
		func(v *DeviceDefaults) { v.Authorization = "" },
	} {
		changed := signed
		mutate(&changed)
		if changed.AuthorizedBy(key) {
			t.Fatal("modified intent retained Core authorization")
		}
	}
	if signed.AuthorizedBy([]byte(strings.Repeat("x", 32))) || signed.AuthorizedBy(nil) {
		t.Fatal("wrong authority key accepted")
	}
}
