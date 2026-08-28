package agentlink

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
)

const maximumBrokerResponse = 16 << 10

type BrokerClient struct {
	URL        string
	Token      string
	HTTPClient *http.Client
}

func (client BrokerClient) AuthenticateCardAKA(ctx context.Context, challenge AKAChallenge) (AKAResponse, error) {
	if err := client.validate(); err != nil {
		return AKAResponse{}, err
	}
	input := BrokerRequest{AKA: challenge}
	if err := input.Validate(); err != nil {
		return AKAResponse{}, err
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return AKAResponse{}, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, client.URL, bytes.NewReader(payload))
	if err != nil {
		return AKAResponse{}, err
	}
	httpRequest.Header.Set("Authorization", "Bearer "+client.Token)
	httpRequest.Header.Set("Content-Type", "application/json")
	response, err := cloneHTTPClient(client.HTTPClient).Do(httpRequest)
	if err != nil {
		return AKAResponse{}, fmt.Errorf("call Agent AKA broker: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maximumBrokerResponse+1))
	if err != nil || len(body) > maximumBrokerResponse {
		return AKAResponse{}, errors.New("invalid Agent AKA broker response size")
	}
	if response.StatusCode == http.StatusOK {
		var result AKAResponse
		if err := decodeStrictBytes(body, &result); err != nil {
			return AKAResponse{}, fmt.Errorf("decode Agent AKA response: %w", err)
		}
		if result.SessionGeneration == "" {
			return AKAResponse{}, errors.New("Agent AKA broker response has no card session generation")
		}
		if err := result.ValidateFor(challenge.requestFor(result.SessionGeneration)); err != nil {
			return AKAResponse{}, err
		}
		if result.Failure != nil {
			return result, result.Failure
		}
		return result, nil
	}
	var failure struct {
		Failure RemoteError `json:"failure"`
	}
	if err := decodeStrictBytes(body, &failure); err != nil || failure.Failure.Validate() != nil {
		return AKAResponse{}, fmt.Errorf("Agent AKA broker returned HTTP %d", response.StatusCode)
	}
	return AKAResponse{}, &failure.Failure
}

func (client BrokerClient) validate() error {
	if len(client.Token) < minimumTokenBytes {
		return errors.New("invalid Agent AKA broker token")
	}
	parsed, err := url.Parse(client.URL)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.RawQuery != "" ||
		parsed.Fragment != "" || parsed.Path != "/v1/agent/aka" {
		return errors.New("Agent AKA broker URL must be exact loopback HTTP path")
	}
	host := parsed.Hostname()
	address := net.ParseIP(strings.Trim(host, "[]"))
	if address == nil || !address.IsLoopback() {
		return errors.New("Agent AKA broker URL must use a literal loopback address")
	}
	return nil
}

func decodeStrictBytes(payload []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("JSON response has trailing content")
	}
	return nil
}
