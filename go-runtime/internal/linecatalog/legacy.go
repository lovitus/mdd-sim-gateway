package linecatalog

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	yaml "go.yaml.in/yaml/v3"
)

const maximumLegacyConfigBytes = 8 << 20
const maximumLegacyDesiredBytes = 1 << 20

type legacyString string

func (value *legacyString) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode {
		return errors.New("expected a scalar value")
	}
	*value = legacyString(node.Value)
	return nil
}

type legacyStringList []string

func (values *legacyStringList) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		for _, value := range strings.Split(node.Value, ",") {
			if value = strings.TrimSpace(value); value != "" {
				*values = append(*values, value)
			}
		}
	case yaml.SequenceNode:
		for _, child := range node.Content {
			if child.Kind != yaml.ScalarNode {
				return errors.New("P-CSCF list contains a non-scalar value")
			}
			*values = append(*values, child.Value)
		}
	default:
		return errors.New("P-CSCF must be a string or list")
	}
	return nil
}

type legacyIMS struct {
	IMPI             legacyString `yaml:"impi"`
	IMPU             legacyString `yaml:"impu"`
	Domain           legacyString `yaml:"domain"`
	AKAAppPreference legacyString `yaml:"aka_app_preference"`
	Network          legacyString `yaml:"network"`
	Server           legacyString `yaml:"server"`
	Expires          int          `yaml:"expires"`
}

type legacyNetwork struct {
	EPDGAddress legacyString     `yaml:"epdg_address"`
	PCSCF       legacyStringList `yaml:"pcscf"`
}

type legacyLine struct {
	ID           legacyString     `yaml:"id"`
	Name         legacyString     `yaml:"name"`
	Enabled      *bool            `yaml:"enabled"`
	SoftDeleted  bool             `yaml:"soft_deleted"`
	ICCID        legacyString     `yaml:"iccid"`
	IMSI         legacyString     `yaml:"imsi"`
	MCC          legacyString     `yaml:"mcc"`
	MNC          legacyString     `yaml:"mnc"`
	IMEI         legacyString     `yaml:"imei"`
	MSISDN       legacyString     `yaml:"msisdn"`
	SMSC         legacyString     `yaml:"smsc"`
	ProxyCountry legacyString     `yaml:"proxy_country"`
	EPDG         legacyString     `yaml:"epdg"`
	PCSCF        legacyStringList `yaml:"pcscf"`
	IMPI         legacyString     `yaml:"impi"`
	IMPU         legacyString     `yaml:"impu"`
	Domain       legacyString     `yaml:"domain"`
	AKAApp       legacyString     `yaml:"aka_app_preference"`
	IMSNetwork   legacyString     `yaml:"ims_network"`
	IMSServer    legacyString     `yaml:"ims_server"`
	IMSExpires   int              `yaml:"ims_expires"`
	Network      legacyNetwork    `yaml:"network"`
	IMS          legacyIMS        `yaml:"ims"`
}

type legacyDocument struct {
	Instances map[string]legacyLine `yaml:"instances"`
}

func ReadLegacy(path string) ([]Line, ImportReceipt, error) {
	var empty ImportReceipt
	path = strings.TrimSpace(path)
	if !filepath.IsAbs(path) {
		return nil, empty, errors.New("legacy configuration path must be absolute")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, empty, err
	}
	if !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > maximumLegacyConfigBytes {
		return nil, empty, errors.New("legacy configuration must be a non-empty regular file no larger than 8 MiB")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, empty, errors.New("legacy configuration contains secrets and must not be accessible by group or others")
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return nil, empty, err
	}
	lines, err := parseLegacy(payload)
	if err != nil {
		return nil, empty, err
	}
	digest := sha256.Sum256(payload)
	return lines, ImportReceipt{
		SourceSHA256: hex.EncodeToString(digest[:]), LineCount: len(lines), ImportedAt: time.Now().UTC(),
	}, nil
}

func parseLegacy(payload []byte) ([]Line, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(payload))
	var document legacyDocument
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode legacy configuration: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("legacy configuration must contain one YAML document")
	}
	if len(document.Instances) == 0 {
		return nil, errors.New("legacy configuration has no instances")
	}
	keys := make([]string, 0, len(document.Instances))
	for key := range document.Instances {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	lines := make([]Line, 0, len(keys))
	for _, key := range keys {
		legacy := document.Instances[key]
		if legacy.SoftDeleted {
			continue
		}
		enabled := true
		if legacy.Enabled != nil {
			enabled = *legacy.Enabled
		}
		lineID := strings.TrimSpace(string(legacy.ID))
		if lineID == "" {
			lineID = strings.TrimSpace(key)
		}
		networkEPDG := first(string(legacy.Network.EPDGAddress), string(legacy.EPDG))
		pcscf := []string(legacy.Network.PCSCF)
		if len(pcscf) == 0 {
			pcscf = []string(legacy.PCSCF)
		}
		line := Line{
			SchemaVersion: SchemaVersion, ID: lineID, Name: string(legacy.Name), Enabled: enabled,
			CardID: string(legacy.ICCID),
			SIM: SIMConfig{IMSI: string(legacy.IMSI), MCC: string(legacy.MCC), MNC: string(legacy.MNC),
				IMEI: string(legacy.IMEI), MSISDN: string(legacy.MSISDN), SMSC: string(legacy.SMSC)},
			Network: NetworkConfig{EPDGAddress: networkEPDG, PCSCF: pcscf, EgressCountry: string(legacy.ProxyCountry)},
			IMS: IMSConfig{
				IMPI:             first(string(legacy.IMS.IMPI), string(legacy.IMPI)),
				IMPU:             first(string(legacy.IMS.IMPU), string(legacy.IMPU)),
				Domain:           first(string(legacy.IMS.Domain), string(legacy.Domain)),
				AKAAppPreference: first(string(legacy.IMS.AKAAppPreference), string(legacy.AKAApp)),
				Network:          first(string(legacy.IMS.Network), string(legacy.IMSNetwork)),
				Server:           first(string(legacy.IMS.Server), string(legacy.IMSServer)),
				Expires:          firstInt(legacy.IMS.Expires, legacy.IMSExpires),
			},
		}
		if err := line.normalizeAndValidate(); err != nil {
			return nil, fmt.Errorf("legacy instance %s: %w", strconv.Quote(key), err)
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return nil, errors.New("legacy configuration has no active instances")
	}
	return lines, nil
}

type legacyDesiredLine struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
	Country string `json:"country"`
}

type legacyDesiredDocument struct {
	Version int                 `json:"version"`
	Lines   []legacyDesiredLine `json:"lines"`
}

// ApplyLegacyDesiredEgress materializes the effective country already computed by
// the legacy control plane. This keeps MCC tables and legacy inference out of the
// new runtime while preserving the exact configured behavior during migration.
func ApplyLegacyDesiredEgress(lines []Line, path string) ([]Line, string, error) {
	path = strings.TrimSpace(path)
	if !filepath.IsAbs(path) {
		return nil, "", errors.New("legacy egress desired path must be absolute")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, "", err
	}
	if !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > maximumLegacyDesiredBytes {
		return nil, "", errors.New("legacy egress desired state must be a non-empty regular file no larger than 1 MiB")
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	var desired legacyDesiredDocument
	if err := decoder.Decode(&desired); err != nil {
		return nil, "", fmt.Errorf("decode legacy egress desired state: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, "", errors.New("legacy egress desired state must contain one JSON document")
	}
	if desired.Version != 1 || len(desired.Lines) == 0 {
		return nil, "", errors.New("legacy egress desired state has an unsupported schema or no lines")
	}
	effective := make(map[string]legacyDesiredLine, len(desired.Lines))
	for _, item := range desired.Lines {
		id := strings.TrimSpace(item.ID)
		country, ok := normalizeCountry(item.Country)
		if !validIdentifier(id) || !ok {
			return nil, "", errors.New("legacy egress desired state contains an invalid line")
		}
		if _, duplicate := effective[id]; duplicate {
			return nil, "", errors.New("legacy egress desired state contains duplicate line IDs")
		}
		item.ID, item.Country = id, country
		effective[id] = item
	}
	result := make([]Line, len(lines))
	for index, input := range lines {
		line := cloneLine(input)
		desiredLine, exists := effective[line.ID]
		if !exists {
			return nil, "", fmt.Errorf("legacy egress desired state has no line %q", line.ID)
		}
		if desiredLine.Enabled != line.Enabled {
			return nil, "", fmt.Errorf("legacy line %q enabled state disagrees with desired state", line.ID)
		}
		country := desiredLine.Country
		if line.Network.EgressCountry != "" && line.Network.EgressCountry != country {
			return nil, "", fmt.Errorf("legacy line %q egress country disagrees with desired state", line.ID)
		}
		if line.Enabled && country == "" {
			return nil, "", fmt.Errorf("enabled legacy line %q has no effective egress country", line.ID)
		}
		line.Network.EgressCountry = country
		if err := line.normalizeAndValidate(); err != nil {
			return nil, "", fmt.Errorf("legacy line %q: %w", line.ID, err)
		}
		result[index] = line
	}
	digest := sha256.Sum256(payload)
	return result, hex.EncodeToString(digest[:]), nil
}

func first(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func firstInt(values ...int) int {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}
