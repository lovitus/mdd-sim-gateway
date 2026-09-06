package adminauth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	CredentialPersistencePath = "/v1/system/admin-credentials"
	localTokenHeader          = "X-MDD-Provider-Apply-Token"
)

type fileCredentialPersister struct {
	path     string
	uid, gid int
}

func (persister *fileCredentialPersister) PersistCredential(payload []byte) error {
	return persistCredentialFile(persister.path, payload, persister.uid, persister.gid)
}

type RemoteCredentialPersister struct {
	token string
	http  *http.Client
}

func NewRemoteCredentialPersister(socketPath, token string) (*RemoteCredentialPersister, error) {
	socketPath, token = filepath.Clean(strings.TrimSpace(socketPath)), strings.TrimSpace(token)
	if !filepath.IsAbs(socketPath) || socketPath == string(filepath.Separator) || len(token) < 32 {
		return nil, errors.New("remote administrator credential persistence configuration is invalid")
	}
	transport := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
	}}
	return &RemoteCredentialPersister{token: token, http: &http.Client{Transport: transport, Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("credential persistence redirect refused")
		}}}, nil
}

func (persister *RemoteCredentialPersister) PersistCredential(payload []byte) error {
	if err := validateCredentialDocument(payload); err != nil {
		return err
	}
	request, err := http.NewRequest(http.MethodPost, "http://mdd-provider-apply"+CredentialPersistencePath,
		bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set(localTokenHeader, persister.token)
	request.Header.Set("Content-Type", "application/json")
	expected := sha256.Sum256(payload)
	response, err := persister.http.Do(request)
	if err != nil {
		if persister.confirmDigest(expected) {
			return nil
		}
		return err
	}
	defer response.Body.Close()
	var result struct {
		SHA256 string `json:"sha256"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 4097))
	if response.StatusCode == http.StatusOK && decoder.Decode(&result) == nil &&
		result.SHA256 == hex.EncodeToString(expected[:]) {
		return nil
	}
	if persister.confirmDigest(expected) {
		return nil
	}
	return errors.New("privileged administrator credential persistence failed")
}

func (persister *RemoteCredentialPersister) confirmDigest(expected [sha256.Size]byte) bool {
	request, err := http.NewRequest(http.MethodGet, "http://mdd-provider-apply"+CredentialPersistencePath, nil)
	if err != nil {
		return false
	}
	request.Header.Set(localTokenHeader, persister.token)
	response, err := persister.http.Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	var result struct {
		SHA256 string `json:"sha256"`
	}
	return response.StatusCode == http.StatusOK &&
		json.NewDecoder(io.LimitReader(response.Body, 4097)).Decode(&result) == nil &&
		result.SHA256 == hex.EncodeToString(expected[:])
}

type CredentialPersistenceHandler struct {
	path     string
	uid, gid int
	mu       sync.Mutex
}

func NewCredentialPersistenceHandler(path string, uid, gid int) (*CredentialPersistenceHandler, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	if !filepath.IsAbs(path) || path == string(filepath.Separator) || uid < 0 || gid < 0 {
		return nil, errors.New("administrator credential persistence target is invalid")
	}
	return &CredentialPersistenceHandler{path: path, uid: uid, gid: gid}, nil
}

func (handler *CredentialPersistenceHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	if request.URL.Path != CredentialPersistencePath || request.URL.RawQuery != "" ||
		request.Method != http.MethodGet && request.Method != http.MethodPost {
		http.Error(response, "method_not_allowed", http.StatusMethodNotAllowed)
		return
	}
	if request.Method == http.MethodGet {
		handler.writeCurrentDigest(response)
		return
	}
	payload, err := io.ReadAll(io.LimitReader(request.Body, maxAuthBytes+1))
	if err != nil || len(payload) == 0 || len(payload) > maxAuthBytes || validateCredentialDocument(payload) != nil {
		http.Error(response, "invalid_admin_credentials", http.StatusBadRequest)
		return
	}
	handler.mu.Lock()
	err = persistCredentialFile(handler.path, payload, handler.uid, handler.gid)
	handler.mu.Unlock()
	if err != nil {
		http.Error(response, "admin_credential_persistence_failed", http.StatusInternalServerError)
		return
	}
	digest := sha256.Sum256(payload)
	response.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(response).Encode(map[string]string{"sha256": hex.EncodeToString(digest[:])})
}

func (handler *CredentialPersistenceHandler) writeCurrentDigest(response http.ResponseWriter) {
	handler.mu.Lock()
	file, err := os.Open(handler.path)
	var payload []byte
	if err == nil {
		payload, err = io.ReadAll(io.LimitReader(file, maxAuthBytes+1))
		err = errors.Join(err, file.Close())
	}
	handler.mu.Unlock()
	if err != nil || len(payload) > maxAuthBytes || validateCredentialDocument(payload) != nil {
		http.Error(response, "admin_credential_digest_unavailable", http.StatusInternalServerError)
		return
	}
	digest := sha256.Sum256(payload)
	response.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(response).Encode(map[string]string{"sha256": hex.EncodeToString(digest[:])})
}

func validateCredentialDocument(payload []byte) error {
	if len(payload) == 0 || len(payload) > maxAuthBytes {
		return errors.New("administrator credential document size is invalid")
	}
	var stored credentialFile
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(&stored); err != nil {
		return errors.New("invalid administrator credential document")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("administrator credential document has trailing data")
	}
	username := strings.TrimSpace(stored.Username)
	salt, saltErr := hex.DecodeString(stored.Salt)
	passwordHash, hashErr := hex.DecodeString(stored.PasswordHash)
	mode := strings.TrimSpace(stored.AgentTokenMode)
	if mode == "" {
		mode = AgentCredentialTransition
	}
	if stored.Version != 1 || username == "" || !utf8.ValidString(username) || utf8.RuneCountInString(username) > 64 ||
		saltErr != nil || len(salt) != 16 || hashErr != nil || len(passwordHash) != 32 ||
		mode != AgentCredentialTransition && mode != AgentCredentialScoped || len(stored.AgentTokens) > maxAgentCredentials {
		return errors.New("administrator credential document is incomplete")
	}
	for rawAgentID, token := range stored.AgentTokens {
		if rawAgentID != strings.TrimSpace(rawAgentID) || !validAgentID(rawAgentID) ||
			token != "" && (token != strings.TrimSpace(token) || len(token) < 32 || len(token) > 512) {
			return errors.New("administrator credential document has an invalid Agent token")
		}
	}
	return nil
}

func persistCredentialFile(path string, payload []byte, uid, gid int) error {
	if err := validateCredentialDocument(payload); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("administrator credential target is not a regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".auth.json-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if uid >= 0 && gid >= 0 {
		if err := temporary.Chown(uid, gid); err != nil {
			_ = temporary.Close()
			return err
		}
	}
	if _, err := temporary.Write(payload); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}
