package egressexec

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/cellulardata"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/egressconfig"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/scopedtoken"
)

const (
	cellularLeaseTTL   = 45 * time.Second
	cellularRenewEvery = 15 * time.Second
	cellularMaxBytes   = uint64(1 << 40)
)

type cellularLease struct {
	ProfileID  string
	CardID     string
	Purpose    string
	SessionID  string    `json:"session_id"`
	LineID     string    `json:"line_id"`
	State      string    `json:"state"`
	Profile    string    `json:"profile"`
	ListenPort int       `json:"listen_port"`
	Username   string    `json:"username"`
	Password   string    `json:"password"`
	ExpiresAt  time.Time `json:"expires_at"`
	MaxBytes   uint64    `json:"max_bytes"`
	UsedBytes  uint64    `json:"used_bytes"`
	nextRenew  time.Time
}

type cellularClient struct {
	base      string
	tokenPath string
	http      *http.Client
	now       func() time.Time
	leases    map[string]*cellularLease
}

func newCellularClient(coreURL, tokenPath string) (*cellularClient, error) {
	parsed, err := url.Parse(strings.TrimSpace(coreURL))
	if err != nil || parsed.Scheme != "http" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" ||
		parsed.Hostname() != "127.0.0.1" || parsed.Port() == "" {
		return nil, errors.New("cellular egress Core IPC must use literal loopback HTTP")
	}
	if address := net.ParseIP(parsed.Hostname()); address == nil || !address.IsLoopback() {
		return nil, errors.New("cellular egress Core IPC is not loopback")
	}
	tokenPath = filepath.Clean(strings.TrimSpace(tokenPath))
	if !filepath.IsAbs(tokenPath) || tokenPath == string(filepath.Separator) {
		return nil, errors.New("cellular egress token path is invalid")
	}
	return &cellularClient{base: strings.TrimRight(parsed.String(), "/"), tokenPath: tokenPath,
		http: &http.Client{Timeout: 20 * time.Second}, now: time.Now, leases: map[string]*cellularLease{}}, nil
}

func (client *cellularClient) authorize(request *http.Request) error {
	token, err := scopedtoken.Read(client.tokenPath)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	return nil
}

func (client *cellularClient) prepare(ctx context.Context, config egressconfig.Config) (egressconfig.Config, error) {
	copy := config
	copy.Profiles = make(map[string]egressconfig.Profile, len(config.Profiles))
	used := map[string]struct{}{}
	if config.Enabled {
		for _, exit := range config.Exits {
			if exit.Enabled && exit.Mode != "direct" {
				used[exit.ProfileID] = struct{}{}
			}
		}
	}
	profileIDs := make([]string, 0, len(config.Profiles))
	for id := range config.Profiles {
		profileIDs = append(profileIDs, id)
	}
	sort.Strings(profileIDs)
	for _, id := range profileIDs {
		profile := config.Profiles[id]
		_, needed := used[id]
		if profile.Type != "cellular_sim" || !needed {
			copy.Profiles[id] = profile
			continue
		}
		lease, err := client.ensure(ctx, id, profile)
		if err != nil {
			// Egress is one applied runtime. If any required cellular profile
			// cannot be prepared, sing-box will not start; retaining another
			// lease would only block modem calls/SMS/policy without an exit.
			client.stopUnused(ctx, map[string]struct{}{})
			return copy, fmt.Errorf("cellular profile %s: %w", id, err)
		}
		copy.Profiles[id] = egressconfig.Profile{Name: profile.Name, Type: "socks5", Server: "127.0.0.1",
			Port: lease.ListenPort, Username: lease.Username, Password: lease.Password, RuntimeMode: "cellular_sim"}
	}
	client.stopUnused(ctx, used)
	return copy, nil
}

func (client *cellularClient) ensure(ctx context.Context, profileID string, profile egressconfig.Profile) (*cellularLease, error) {
	now := client.now().UTC()
	if current := client.leases[profileID]; current != nil {
		if current.CardID != profile.SIMICCID {
			if err := client.stop(ctx, current); err != nil {
				return nil, err
			}
			delete(client.leases, profileID)
		} else if now.Before(current.nextRenew) {
			return current, nil
		}
	}
	purpose := "egress:" + profileID
	expiresAt := now.Add(cellularLeaseTTL)
	operationID, err := randomCellularOperation("renew")
	if err != nil {
		return nil, err
	}
	input := map[string]any{"card_id": profile.SIMICCID, "purpose": purpose, "operation_id": operationID,
		"expires_at": expiresAt, "max_bytes": cellularMaxBytes}
	payload, _ := json.Marshal(input)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.base+cellulardata.InternalPath, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	if err := client.authorize(request); err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.http.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return nil, errors.New("Core rejected cellular egress lease")
	}
	var lease cellularLease
	decoder := json.NewDecoder(io.LimitReader(response.Body, 8193))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&lease) != nil || decoder.Decode(&struct{}{}) != io.EOF || lease.SessionID == "" ||
		lease.State != "ready" || lease.ListenPort < 1 || lease.Username == "" || lease.Password == "" ||
		lease.ExpiresAt.Before(expiresAt.Add(-time.Second)) {
		return nil, errors.New("Core returned an invalid cellular egress lease")
	}
	lease.ProfileID, lease.CardID, lease.Purpose, lease.nextRenew = profileID, profile.SIMICCID, purpose, now.Add(cellularRenewEvery)
	client.leases[profileID] = &lease
	return &lease, nil
}

func (client *cellularClient) stopUnused(ctx context.Context, used map[string]struct{}) {
	for id, lease := range client.leases {
		if _, keep := used[id]; keep {
			continue
		}
		_ = client.stop(ctx, lease)
		delete(client.leases, id)
	}
}

func (client *cellularClient) stop(ctx context.Context, lease *cellularLease) error {
	if lease == nil {
		return nil
	}
	payload, _ := json.Marshal(map[string]string{"card_id": lease.CardID, "session_id": lease.SessionID, "purpose": lease.Purpose})
	request, err := http.NewRequestWithContext(ctx, http.MethodDelete, client.base+cellulardata.InternalPath, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	if err := client.authorize(request); err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode != http.StatusNoContent {
		return errors.New("Core rejected cellular egress stop")
	}
	return nil
}

func (client *cellularClient) close() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client.stopUnused(ctx, map[string]struct{}{})
}

func randomCellularOperation(prefix string) (string, error) {
	buffer := make([]byte, 18)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return prefix + "-" + base64.RawURLEncoding.EncodeToString(buffer), nil
}
