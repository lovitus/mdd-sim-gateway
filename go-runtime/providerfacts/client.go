package providerfacts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
)

type Client struct {
	URL        string
	Token      string
	HTTPClient *http.Client
}

func (client Client) Report(ctx context.Context, snapshot vowifiipc.Snapshot) error {
	if err := client.Validate(); err != nil {
		return err
	}
	if err := snapshot.Validate(); err != nil {
		return err
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, client.URL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+client.Token)
	request.Header.Set("Content-Type", "application/json")
	httpClient := &http.Client{}
	if client.HTTPClient != nil {
		clone := *client.HTTPClient
		httpClient = &clone
	}
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if _, err := io.Copy(io.Discard, io.LimitReader(response.Body, 1024)); err != nil {
		return err
	}
	if response.StatusCode != http.StatusNoContent {
		return errors.New("provider facts snapshot was rejected")
	}
	return nil
}

func (client Client) Validate() error {
	if len(client.Token) < 32 {
		return errors.New("invalid provider facts token")
	}
	parsed, err := url.Parse(client.URL)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.RawQuery != "" ||
		parsed.Fragment != "" || parsed.Path != "/v1/provider/facts" || parsed.Port() == "" {
		return errors.New("provider facts URL must be exact loopback HTTP path")
	}
	address := net.ParseIP(strings.Trim(parsed.Hostname(), "[]"))
	if address == nil || !address.IsLoopback() {
		return errors.New("provider facts URL must use a literal loopback address")
	}
	return nil
}
