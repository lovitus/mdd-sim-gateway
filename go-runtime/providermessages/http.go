package providermessages

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/mediaauth"
)

const maximumEventBytes = 72 << 10

type Handler struct {
	providers *mediaauth.ProviderDirectory
	store     *Store
	tokenHash [sha256.Size]byte
	now       func() time.Time
}

func NewHandler(providers *mediaauth.ProviderDirectory, store *Store, token string) (*Handler, error) {
	if providers == nil || store == nil || len(token) < 32 {
		return nil, errors.New("invalid provider message handler configuration")
	}
	return &Handler{providers: providers, store: store, tokenHash: sha256.Sum256([]byte(token)), now: time.Now}, nil
}

func (handler *Handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/v1/provider/messages" || request.URL.RawQuery != "" {
		http.NotFound(response, request)
		return
	}
	if request.Method != http.MethodPost {
		messageFailure(response, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if !messageLoopbackRemote(request.RemoteAddr) {
		messageFailure(response, http.StatusForbidden, "loopback_required")
		return
	}
	if !handler.authorized(request.Header.Get("Authorization")) {
		messageFailure(response, http.StatusUnauthorized, "unauthorized")
		return
	}
	contentType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		messageFailure(response, http.StatusBadRequest, "invalid_message_event")
		return
	}
	var event Event
	if err := decodeMessageEvent(request.Body, &event); err != nil || event.Validate() != nil {
		messageFailure(response, http.StatusBadRequest, "invalid_message_event")
		return
	}
	err = handler.providers.UseCurrent(request.Context(), event.LineID, func(provider mediaauth.Provider) error {
		if provider.ProviderID != event.ProviderID || provider.Generation != event.ProcessGeneration {
			return mediaauth.ErrProviderUnavailable
		}
		_, _, err := handler.store.Accept(event, handler.now().UTC())
		return err
	})
	switch {
	case err == nil:
		response.WriteHeader(http.StatusNoContent)
	case errors.Is(err, mediaauth.ErrProviderUnavailable), errors.Is(err, ErrConflict):
		messageFailure(response, http.StatusConflict, "stale_provider_message")
	default:
		messageFailure(response, http.StatusInternalServerError, "message_persist_failed")
	}
}

func (handler *Handler) authorized(header string) bool {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	presented := sha256.Sum256([]byte(strings.TrimPrefix(header, prefix)))
	return subtle.ConstantTimeCompare(presented[:], handler.tokenHash[:]) == 1
}

type Client struct {
	URL        string
	Token      string
	HTTPClient *http.Client
}

func (client Client) Report(ctx context.Context, event Event) error {
	if err := client.Validate(); err != nil {
		return err
	}
	if err := event.Validate(); err != nil {
		return err
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.URL, bytes.NewReader(payload))
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
		return errors.New("provider message event was rejected")
	}
	return nil
}

func (client Client) Validate() error {
	if len(client.Token) < 32 {
		return errors.New("invalid provider message token")
	}
	parsed, err := url.Parse(client.URL)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.RawQuery != "" ||
		parsed.Fragment != "" || parsed.Path != "/v1/provider/messages" || parsed.Port() == "" {
		return errors.New("provider message URL must be exact loopback HTTP path")
	}
	address := net.ParseIP(strings.Trim(parsed.Hostname(), "[]"))
	if address == nil || !address.IsLoopback() {
		return errors.New("provider message URL must use a literal loopback address")
	}
	return nil
}

type PublicHandler struct{ store *Store }

func NewPublicHandler(store *Store) (*PublicHandler, error) {
	if store == nil {
		return nil, errors.New("nil provider message store")
	}
	return &PublicHandler{store: store}, nil
}

func (handler *PublicHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		messageFailure(response, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	limit := 100
	if value := strings.TrimSpace(request.URL.Query().Get("limit")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			messageFailure(response, http.StatusBadRequest, "invalid_limit")
			return
		}
		limit = parsed
	}
	records, err := handler.store.List(request.URL.Query().Get("line_id"), limit)
	if err != nil {
		messageFailure(response, http.StatusInternalServerError, "message_read_failed")
		return
	}
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(response).Encode(map[string]any{"messages": records})
}

func decodeMessageEvent(body io.Reader, target *Event) error {
	payload, err := io.ReadAll(io.LimitReader(body, maximumEventBytes+1))
	if err != nil || len(payload) == 0 || len(payload) > maximumEventBytes {
		return errors.New("invalid provider message size")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("provider message has trailing JSON")
	}
	return nil
}

func messageLoopbackRemote(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return false
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func messageFailure(response http.ResponseWriter, status int, code string) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(map[string]string{"code": code})
}
