// Package linecatalog owns the durable desired configuration for MDD lines.
// Runtime observations, Agent attachment generations and recovery state do not
// belong in this store.
package linecatalog

import (
	"errors"
	"sort"
	"strings"
)

const SchemaVersion = 1

type SIMConfig struct {
	IMSI   string `json:"imsi"`
	MCC    string `json:"mcc"`
	MNC    string `json:"mnc"`
	IMEI   string `json:"imei,omitempty"`
	MSISDN string `json:"msisdn,omitempty"`
	SMSC   string `json:"smsc,omitempty"`
}

type NetworkConfig struct {
	EPDGAddress   string   `json:"epdg_address,omitempty"`
	PCSCF         []string `json:"pcscf,omitempty"`
	EgressCountry string   `json:"egress_country,omitempty"`
}

type IMSConfig struct {
	IMPI              string `json:"impi,omitempty"`
	IMPU              string `json:"impu,omitempty"`
	Domain            string `json:"domain,omitempty"`
	UserAgent         string `json:"user_agent,omitempty"`
	AccessNetworkInfo string `json:"access_network_info,omitempty"`
	VisitedNetworkID  string `json:"visited_network_id,omitempty"`
	AccessType        string `json:"access_type,omitempty"`
	UserEqualsPhone   bool   `json:"user_equals_phone,omitempty"`
	AKAAppPreference  string `json:"aka_app_preference,omitempty"`
	Network           string `json:"network,omitempty"`
	Server            string `json:"server,omitempty"`
	Expires           int    `json:"expires,omitempty"`
}

type Line struct {
	SchemaVersion int           `json:"schema_version"`
	ID            string        `json:"id"`
	Name          string        `json:"name,omitempty"`
	Enabled       bool          `json:"enabled"`
	CardID        string        `json:"card_id"`
	SIM           SIMConfig     `json:"sim"`
	Network       NetworkConfig `json:"network"`
	IMS           IMSConfig     `json:"ims"`
	Deleted       bool          `json:"deleted,omitempty"`
}

type Snapshot struct {
	SchemaVersion int    `json:"schema_version"`
	Revision      uint64 `json:"revision"`
	Lines         []Line `json:"lines"`
}

func (line *Line) normalizeAndValidate() error {
	if line == nil {
		return errors.New("line is nil")
	}
	line.ID = strings.TrimSpace(line.ID)
	line.Name = strings.TrimSpace(line.Name)
	line.CardID = digitsOnly(line.CardID)
	line.SIM.IMSI = digitsOnly(line.SIM.IMSI)
	line.SIM.MCC = digitsOnly(line.SIM.MCC)
	line.SIM.MNC = digitsOnly(line.SIM.MNC)
	line.SIM.IMEI = digitsOnly(line.SIM.IMEI)
	line.SIM.MSISDN = normalizeNumber(line.SIM.MSISDN)
	line.SIM.SMSC = normalizeNumber(line.SIM.SMSC)
	line.Network.EPDGAddress = strings.TrimSpace(line.Network.EPDGAddress)
	if country, ok := normalizeCountry(line.Network.EgressCountry); ok {
		line.Network.EgressCountry = country
	} else {
		return errors.New("line egress country is invalid")
	}
	line.IMS.IMPI = strings.TrimSpace(line.IMS.IMPI)
	line.IMS.IMPU = strings.TrimSpace(line.IMS.IMPU)
	line.IMS.Domain = strings.TrimSpace(line.IMS.Domain)
	line.IMS.UserAgent = strings.TrimSpace(line.IMS.UserAgent)
	line.IMS.AccessNetworkInfo = strings.TrimSpace(line.IMS.AccessNetworkInfo)
	line.IMS.VisitedNetworkID = strings.TrimSpace(line.IMS.VisitedNetworkID)
	line.IMS.AccessType = strings.TrimSpace(line.IMS.AccessType)
	line.IMS.AKAAppPreference = strings.ToLower(strings.TrimSpace(line.IMS.AKAAppPreference))
	line.IMS.Network = strings.ToLower(strings.TrimSpace(line.IMS.Network))
	line.IMS.Server = strings.TrimSpace(line.IMS.Server)
	line.Network.PCSCF = cleanList(line.Network.PCSCF)
	if line.SchemaVersion == 0 {
		line.SchemaVersion = SchemaVersion
	}
	if line.SchemaVersion != SchemaVersion || !validIdentifier(line.ID) || len(line.Name) > 256 {
		return errors.New("line schema, id, or name is invalid")
	}
	if !digitsBetween(line.CardID, 4, 32) {
		return errors.New("line ICCID is invalid")
	}
	validSIMIdentity := digitsBetween(line.SIM.IMSI, 5, 18) && digitsBetween(line.SIM.MCC, 3, 3) &&
		digitsBetween(line.SIM.MNC, 2, 3)
	if line.Enabled && !validSIMIdentity {
		return errors.New("enabled line IMSI, MCC, or MNC is invalid")
	}
	if !line.Enabled && ((line.SIM.IMSI != "" && !digitsBetween(line.SIM.IMSI, 5, 18)) ||
		(line.SIM.MCC != "" && !digitsBetween(line.SIM.MCC, 3, 3)) ||
		(line.SIM.MNC != "" && !digitsBetween(line.SIM.MNC, 2, 3))) {
		return errors.New("disabled line IMSI, MCC, or MNC is invalid")
	}
	if (line.SIM.IMEI != "" && !digitsBetween(line.SIM.IMEI, 14, 16)) ||
		(line.SIM.MSISDN != "" && !validNumber(line.SIM.MSISDN)) ||
		(line.SIM.SMSC != "" && !validNumber(line.SIM.SMSC)) {
		return errors.New("line IMEI, MSISDN, or SMSC is invalid")
	}
	if !validEndpoint(line.Network.EPDGAddress) || len(line.Network.PCSCF) > 16 {
		return errors.New("line ePDG or P-CSCF configuration is invalid")
	}
	for _, endpoint := range line.Network.PCSCF {
		if !validEndpoint(endpoint) {
			return errors.New("line P-CSCF endpoint is invalid")
		}
	}
	if (line.IMS.AKAAppPreference != "" && line.IMS.AKAAppPreference != "usim" && line.IMS.AKAAppPreference != "isim") ||
		(line.IMS.Network != "" && line.IMS.Network != "udp" && line.IMS.Network != "tcp") ||
		line.IMS.Expires < 0 || line.IMS.Expires > 86400 {
		return errors.New("line IMS application, network, or expiry is invalid")
	}
	if (line.IMS.IMPI != "" || line.IMS.IMPU != "" || line.IMS.Domain != "") &&
		(line.IMS.IMPI == "" || line.IMS.IMPU == "" || line.IMS.Domain == "") {
		return errors.New("explicit IMS identity must include IMPI, IMPU, and domain")
	}
	for _, value := range []string{
		line.IMS.IMPI, line.IMS.IMPU, line.IMS.Domain, line.IMS.UserAgent, line.IMS.Server,
		line.IMS.AccessNetworkInfo, line.IMS.VisitedNetworkID, line.IMS.AccessType,
	} {
		if len(value) > 512 || containsControl(value) {
			return errors.New("line IMS endpoint or identity is invalid")
		}
	}
	return nil
}

func normalizeCountry(value string) (string, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "", true
	}
	if len(value) != 2 || value[0] < 'a' || value[0] > 'z' || value[1] < 'a' || value[1] > 'z' {
		return "", false
	}
	return value, true
}

func cloneLine(line Line) Line {
	line.Network.PCSCF = append([]string(nil), line.Network.PCSCF...)
	return line
}

func cleanList(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func validIdentifier(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' ||
			strings.ContainsRune("-_.:", char) {
			continue
		}
		return false
	}
	return true
}

func digitsOnly(value string) string {
	var result strings.Builder
	for _, char := range strings.TrimSpace(value) {
		if char >= '0' && char <= '9' {
			result.WriteRune(char)
		} else if char != ' ' && char != '-' && char != '\t' {
			result.WriteRune(char)
		}
	}
	return result.String()
}

func digitsBetween(value string, minimum, maximum int) bool {
	if len(value) < minimum || len(value) > maximum {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func normalizeNumber(value string) string {
	value = strings.TrimSpace(value)
	prefix := ""
	if strings.HasPrefix(value, "+") {
		prefix = "+"
		value = strings.TrimPrefix(value, "+")
	}
	return prefix + digitsOnly(value)
}

func validNumber(value string) bool {
	digits := strings.TrimPrefix(value, "+")
	return digitsBetween(digits, 1, 32)
}

func validEndpoint(value string) bool {
	return len(value) <= 512 && !containsControl(value) && !strings.ContainsAny(value, " \t\r\n")
}

func containsControl(value string) bool {
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			return true
		}
	}
	return false
}
