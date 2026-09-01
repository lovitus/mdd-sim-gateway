package windowsmbn

import (
	"encoding/xml"
	"strings"
	"testing"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentpolicy"
)

func TestWindowsProfileXMLUsesV1SchemaEscapesSecretsAndIsNotDefault(t *testing.T) {
	payload, err := windowsProfileXML(agentpolicy.Profile{
		Name: "MDD<&>", APN: "internet&ims", Auth: "MSCHAPV2",
		Username: "user<&>", Password: `secret<&>`,
	}, "234100000000001")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(payload, "<IsDefault>true</IsDefault>") ||
		!strings.Contains(payload, "<IsDefault>false</IsDefault>") ||
		!strings.Contains(payload, "<AuthProtocol>MsCHAPv2</AuthProtocol>") ||
		!strings.Contains(payload, "MDD&lt;&amp;&gt;") ||
		!strings.Contains(payload, "secret&lt;&amp;&gt;") {
		t.Fatalf("profile XML=%s", payload)
	}
	var document struct {
		XMLName xml.Name
		Name    string `xml:"Name"`
		Context struct {
			AccessString string `xml:"AccessString"`
			Auth         string `xml:"AuthProtocol"`
		} `xml:"Context"`
	}
	if err := xml.Unmarshal([]byte(payload), &document); err != nil ||
		document.XMLName.Space != "http://www.microsoft.com/networking/WWAN/profile/v1" ||
		document.Name != "MDD<&>" || document.Context.AccessString != "internet&ims" ||
		document.Context.Auth != "MsCHAPv2" {
		t.Fatalf("decoded=%+v err=%v", document, err)
	}
}

func TestWindowsProfileXMLRejectsInvalidAuthAndIdentity(t *testing.T) {
	base := agentpolicy.Profile{Name: "MDD", APN: "internet", Auth: "NONE"}
	if _, err := windowsProfileXML(base, "123"); err == nil {
		t.Fatal("short IMSI was accepted")
	}
	base.Auth = "AUTO"
	if _, err := windowsProfileXML(base, "234100000000001"); err == nil {
		t.Fatal("unsupported v1 authentication protocol was accepted")
	}
}
