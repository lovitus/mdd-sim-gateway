package mediaauth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
)

const maximumRegistrationBytes = 16 << 10

type RegistrationHandler struct {
	directory *ProviderDirectory
	tokenHash [sha256.Size]byte
}

type deregistration struct {
	LineID     string `json:"line_id"`
	Generation string `json:"generation"`
}

func NewRegistrationHandler(directory *ProviderDirectory, token string) (*RegistrationHandler, error) {
	if directory == nil || len(token) < 32 {
		return nil, errors.New("invalid media provider registration configuration")
	}
	return &RegistrationHandler{directory: directory, tokenHash: sha256.Sum256([]byte(token))}, nil
}

func (handler *RegistrationHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/v1/media/providers" || request.URL.RawQuery != "" {
		http.NotFound(response, request)
		return
	}
	if !literalLoopbackRemote(request.RemoteAddr) {
		writeRegistration(response, http.StatusForbidden, "loopback_required")
		return
	}
	if !handler.authorized(request.Header.Get("Authorization")) {
		writeRegistration(response, http.StatusUnauthorized, "unauthorized")
		return
	}
	switch request.Method {
	case http.MethodPut:
		var provider Provider
		if decodeRegistration(request.Body, &provider) != nil {
			writeRegistration(response, http.StatusBadRequest, "invalid_provider")
			return
		}
		if err := handler.directory.Replace(provider); err != nil {
			if errors.Is(err, ErrProviderGenerationReused) {
				writeRegistration(response, http.StatusConflict, "provider_generation_reused")
				return
			}
			writeRegistration(response, http.StatusBadRequest, "invalid_provider")
			return
		}
		response.WriteHeader(http.StatusNoContent)
	case http.MethodDelete:
		var input deregistration
		if decodeRegistration(request.Body, &input) != nil || !validID(input.LineID) || !validID(input.Generation) {
			writeRegistration(response, http.StatusBadRequest, "invalid_provider")
			return
		}
		handler.directory.Remove(input.LineID, input.Generation)
		response.WriteHeader(http.StatusNoContent)
	default:
		writeRegistration(response, http.StatusMethodNotAllowed, "method_not_allowed")
	}
}

func (handler *RegistrationHandler) authorized(header string) bool {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	presented := sha256.Sum256([]byte(strings.TrimPrefix(header, prefix)))
	return subtle.ConstantTimeCompare(presented[:], handler.tokenHash[:]) == 1
}

type RegistrationClient struct {
	URL        string
	Token      string
	HTTPClient *http.Client
}

func (client RegistrationClient) Register(ctx context.Context, provider Provider) error {
	return client.request(ctx, http.MethodPut, provider)
}

func (client RegistrationClient) Remove(ctx context.Context, lineID, generation string) error {
	return client.request(ctx, http.MethodDelete, deregistration{LineID: lineID, Generation: generation})
}

func (client RegistrationClient) request(ctx context.Context, method string, value any) error {
	if err := client.Validate(); err != nil {
		return err
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, method, client.URL, bytes.NewReader(payload))
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
	_, readErr := io.Copy(io.Discard, io.LimitReader(response.Body, 1024))
	if readErr != nil {
		return readErr
	}
	if response.StatusCode != http.StatusNoContent {
		return errors.New("media provider registration was rejected")
	}
	return nil
}

func (client RegistrationClient) Validate() error {
	if len(client.Token) < 32 {
		return errors.New("invalid media provider registration token")
	}
	parsed, err := url.Parse(client.URL)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.RawQuery != "" ||
		parsed.Fragment != "" || parsed.Path != "/v1/media/providers" || parsed.Port() == "" {
		return errors.New("media provider registration URL must be exact loopback HTTP path")
	}
	address := net.ParseIP(strings.Trim(parsed.Hostname(), "[]"))
	if address == nil || !address.IsLoopback() {
		return errors.New("media provider registration URL must use a literal loopback address")
	}
	return nil
}

func decodeRegistration(body io.Reader, target any) error {
	payload, err := io.ReadAll(io.LimitReader(body, maximumRegistrationBytes+1))
	if err != nil || len(payload) == 0 || len(payload) > maximumRegistrationBytes {
		return errors.New("invalid registration request size")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("registration request has trailing JSON")
	}
	return nil
}

func literalLoopbackRemote(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return false
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func writeRegistration(response http.ResponseWriter, status int, code string) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(map[string]string{"code": code})
}
