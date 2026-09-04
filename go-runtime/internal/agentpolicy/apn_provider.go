package agentpolicy

import (
	_ "embed"
	"encoding/xml"
	"strings"
	"sync"
)

// serviceproviders.xml is the public-domain mobile-broadband-provider-info
// dataset used by NetworkManager and ModemManager. It is advisory data, not an
// automatic provisioning authority.
//
//go:embed serviceproviders.xml
var providerXML []byte

type providerDocument struct {
	Countries []providerCountry `xml:"country"`
}
type providerCountry struct {
	Providers []providerEntry `xml:"provider"`
}
type providerEntry struct {
	Name string      `xml:"name"`
	GSM  providerGSM `xml:"gsm"`
}
type providerGSM struct {
	Networks []providerNetwork `xml:"network-id"`
	APNs     []providerAPN     `xml:"apn"`
}
type providerNetwork struct {
	MCC string `xml:"mcc,attr"`
	MNC string `xml:"mnc,attr"`
}
type providerAPN struct {
	Value     string `xml:"value,attr"`
	UsageAttr string `xml:"usage,attr"`
	Usage     struct {
		Type string `xml:"type,attr"`
	} `xml:"usage"`
	Name string `xml:"name"`
}

var providerTable = struct {
	sync.Once
	values map[string][]ProfileView
}{}

func ProviderAPNCandidates(imsi string) []ProfileView {
	imsi = apnDigits(imsi)
	if len(imsi) < 5 {
		return []ProfileView{}
	}
	providerTable.Do(loadProviderTable)
	seen := map[string]struct{}{}
	result := []ProfileView{}
	for _, mncLength := range []int{3, 2} {
		if len(imsi) < 3+mncLength {
			continue
		}
		for _, profile := range providerTable.values[imsi[:3]+"/"+imsi[3:3+mncLength]] {
			key := strings.ToLower(profile.APN)
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			result = append(result, profile)
		}
	}
	return result
}

func loadProviderTable() {
	providerTable.values = map[string][]ProfileView{}
	var document providerDocument
	if xml.Unmarshal(providerXML, &document) != nil {
		return
	}
	for _, country := range document.Countries {
		for _, provider := range country.Providers {
			for _, apn := range provider.GSM.APNs {
				value := strings.TrimSpace(apn.Value)
				usage := strings.TrimSpace(apn.UsageAttr)
				if usage == "" {
					usage = strings.TrimSpace(apn.Usage.Type)
				}
				if value == "" || len(value) > 100 || usage != "" && usage != "internet" {
					continue
				}
				name := strings.TrimSpace(apn.Name)
				if name == "" {
					name = strings.TrimSpace(provider.Name)
				}
				if name == "" {
					name = value
				}
				if len(name) > 80 {
					name = name[:80]
				}
				profile := ProfileView{Name: name + " (" + value + ")", APN: value, Auth: "NONE",
					Source: "provider", PDPType: "IP", System: true}
				if len(profile.Name) > 100 {
					profile.Name = profile.Name[:100]
				}
				for _, network := range provider.GSM.Networks {
					mcc, mnc := apnDigits(network.MCC), apnDigits(network.MNC)
					if len(mcc) == 3 && (len(mnc) == 2 || len(mnc) == 3) {
						key := mcc + "/" + mnc
						providerTable.values[key] = append(providerTable.values[key], profile)
					}
				}
			}
		}
	}
}

func apnDigits(value string) string {
	var result strings.Builder
	for _, character := range value {
		if character >= '0' && character <= '9' {
			result.WriteRune(character)
		}
	}
	return result.String()
}
