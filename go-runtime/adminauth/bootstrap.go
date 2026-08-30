package adminauth

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/scrypt"
)

const (
	credentialSaltBytes = 16
	credentialHashBytes = 32
)

// MarshalBootstrapCredential creates the existing version-1 auth.json format.
// Callers remain responsible for publishing the returned payload as a private,
// no-replace file. The password is accepted as bytes so a bootstrap command
// never needs to place it in argv or an environment variable.
func MarshalBootstrapCredential(username string, password []byte, agentToken string, entropy io.Reader) ([]byte, error) {
	username = strings.TrimSpace(username)
	agentToken = strings.TrimSpace(agentToken)
	if username == "" || utf8.RuneCountInString(username) > 64 || strings.IndexFunc(username, unicode.IsControl) >= 0 {
		return nil, errors.New("administrator username must contain 1 to 64 characters without control whitespace")
	}
	if len(password) == 0 || !utf8.Valid(password) || utf8.RuneCount(password) > 256 {
		return nil, errors.New("administrator password must contain 1 to 256 valid UTF-8 characters")
	}
	if len(agentToken) < 32 {
		return nil, errors.New("Agent token must contain at least 32 bytes")
	}
	if entropy == nil {
		entropy = rand.Reader
	}
	salt := make([]byte, credentialSaltBytes)
	if _, err := io.ReadFull(entropy, salt); err != nil {
		return nil, err
	}
	derived, err := scrypt.Key(password, salt, 1<<15, 8, 1, credentialHashBytes)
	if err != nil {
		return nil, err
	}
	defer clear(derived)
	payload, err := json.MarshalIndent(credentialFile{
		Version: 1, Username: username, Salt: hex.EncodeToString(salt),
		PasswordHash: hex.EncodeToString(derived), AgentToken: agentToken,
	}, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(payload, '\n'), nil
}
