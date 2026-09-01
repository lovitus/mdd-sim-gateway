package windowsmbn

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"strings"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentpolicy"
)

func windowsProfileXML(profile agentpolicy.Profile, subscriberID string) (string, error) {
	if strings.TrimSpace(profile.Name) == "" || strings.TrimSpace(profile.APN) == "" ||
		len(subscriberID) < 14 || len(subscriberID) > 15 {
		return "", errors.New("profile name, APN, and IMSI are required")
	}
	auth := strings.ToUpper(strings.TrimSpace(profile.Auth))
	if auth == "MSCHAPV2" {
		auth = "MsCHAPv2"
	}
	if auth != "NONE" && auth != "PAP" && auth != "CHAP" && auth != "MsCHAPv2" {
		return "", errors.New("unsupported Windows MBN authentication protocol")
	}
	var output bytes.Buffer
	escape := func(value string) string {
		var escaped bytes.Buffer
		_ = xml.EscapeText(&escaped, []byte(value))
		return escaped.String()
	}
	credentials := ""
	if profile.Username != "" || profile.Password != "" {
		credentials = "<UserLogonCred><UserName>" + escape(profile.Username) + "</UserName><Password>" +
			escape(profile.Password) + "</Password></UserLogonCred>"
	}
	fmt.Fprintf(&output, `<?xml version="1.0"?><MBNProfile xmlns="http://www.microsoft.com/networking/WWAN/profile/v1"><Name>%s</Name><IsDefault>false</IsDefault><ProfileCreationType>UserProvisioned</ProfileCreationType><SubscriberID>%s</SubscriberID><AutoConnectOnInternet>false</AutoConnectOnInternet><ConnectionMode>manual</ConnectionMode><Context><AccessString>%s</AccessString>%s<Compression>DISABLE</Compression><AuthProtocol>%s</AuthProtocol></Context></MBNProfile>`,
		escape(strings.TrimSpace(profile.Name)), subscriberID, escape(strings.TrimSpace(profile.APN)), credentials, auth)
	return output.String(), nil
}

func normalizeWindowsAuth(value string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	if value == "MSCHAPV2" {
		return "MSCHAPV2"
	}
	if value == "PAP" || value == "CHAP" {
		return value
	}
	return "NONE"
}
