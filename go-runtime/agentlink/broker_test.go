package agentlink

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const brokerToken = "broker-0123456789abcdef0123456789abcdef"

func TestBrokerClientRoutesThroughAgentWSS(t *testing.T) {
	wss, _ := NewServer(TokenResolverFunc(func(context.Context, string) (string, error) {
		return testToken, nil
	}))
	wssServer := httptest.NewServer(wss)
	defer wssServer.Close()
	linkContext, stopLink := context.WithCancel(context.Background())
	linkDone := make(chan error, 1)
	go func() {
		linkDone <- (Client{
			URL:   strings.Replace(wssServer.URL, "http://", "ws://", 1) + "/agent",
			Token: testToken, Hello: Hello{SchemaVersion: 1, AgentID: "broker-agent", ProcessGeneration: "broker-process"},
			Authenticator: &fakeAuthenticator{}, OperationTimeout: time.Second,
		}).Run(linkContext)
	}()
	defer func() { stopLink(); <-linkDone }()

	api, err := NewBrokerAPI(wss, brokerToken, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	apiServer := httptest.NewServer(api)
	defer apiServer.Close()
	client := BrokerClient{
		URL: apiServer.URL + "/v1/agent/aka", Token: brokerToken, HTTPClient: apiServer.Client(),
	}
	request := AKARequest{
		OperationID: "broker-op", SessionGeneration: "broker-card", CardID: "8901",
		Application: AKAApplicationUSIM, RAND: make([]byte, 16), AUTN: make([]byte, 16),
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		result, callErr := client.AuthenticateAKA(context.Background(), "broker-agent", "broker-process", request)
		var remote *RemoteError
		if errors.As(callErr, &remote) && remote.Code == "agent_offline" && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
			continue
		}
		if callErr != nil || result.SW1 != 0x90 {
			t.Fatalf("broker AKA result=%+v error=%v", result, callErr)
		}
		break
	}
}

func TestBrokerAPIRejectsRemoteUnknownAndWrongToken(t *testing.T) {
	broker := brokerFunc(func(context.Context, string, string, AKARequest) (AKAResponse, error) {
		return AKAResponse{}, errors.New("must not execute")
	})
	api, _ := NewBrokerAPI(broker, brokerToken, time.Second)
	valid := `{"agent_id":"agent","process_generation":"process","aka":{"operation_id":"op","session_generation":"session","card_id":"1","application":"usim","rand":"AAAAAAAAAAAAAAAAAAAAAA==","autn":"AAAAAAAAAAAAAAAAAAAAAA=="}}`
	for name, testCase := range map[string]struct {
		remote string
		token  string
		body   string
		want   int
	}{
		"remote":  {"192.0.2.2:1234", brokerToken, valid, http.StatusForbidden},
		"token":   {"127.0.0.1:1234", "wrong-wrong-wrong-wrong-wrong-000", valid, http.StatusUnauthorized},
		"unknown": {"127.0.0.1:1234", brokerToken, strings.TrimSuffix(valid, "}") + `,"future":true}`, http.StatusBadRequest},
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/v1/agent/aka", bytes.NewBufferString(testCase.body))
			request.RemoteAddr = testCase.remote
			request.Header.Set("Authorization", "Bearer "+testCase.token)
			response := httptest.NewRecorder()
			api.ServeHTTP(response, request)
			if response.Code != testCase.want {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestBrokerClientRequiresLiteralLoopback(t *testing.T) {
	client := BrokerClient{URL: "http://localhost/v1/agent/aka", Token: brokerToken}
	if err := client.validate(); err == nil {
		t.Fatal("DNS loopback broker URL accepted")
	}
}

type brokerFunc func(context.Context, string, string, AKARequest) (AKAResponse, error)

func (function brokerFunc) AuthenticateAKA(ctx context.Context, agentID, generation string,
	request AKARequest) (AKAResponse, error) {
	return function(ctx, agentID, generation, request)
}
