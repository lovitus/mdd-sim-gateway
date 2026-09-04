package agentpolicy

import (
	"regexp"
	"strconv"
	"strings"
)

var pdpContextLine = regexp.MustCompile(`(?im)^\s*\+CGDCONT:\s*(\d+)\s*,\s*"([^"]*)"\s*,\s*"([^"]*)"`)

var reservedAPNLeaves = map[string]struct{}{
	"ims": {}, "sos": {}, "emergency": {}, "mms": {}, "supl": {}, "xcap": {},
}

// ParsePDPContexts extracts read-only Internet APN candidates reported by a
// modem. IMS/emergency/service contexts are deliberately not offered as data
// profiles. Returned values are suggestions only and never become desired
// state without an explicit profile save.
func ParsePDPContexts(payload []byte) []ProfileView {
	matches := pdpContextLine.FindAllSubmatch(payload, -1)
	result := make([]ProfileView, 0, len(matches))
	seen := map[string]struct{}{}
	for _, match := range matches {
		cid, err := strconv.Atoi(string(match[1]))
		pdpType := strings.ToUpper(strings.TrimSpace(string(match[2])))
		apn := strings.TrimSpace(string(match[3]))
		leaf := strings.ToLower(strings.SplitN(apn, ".", 2)[0])
		if err != nil || cid < 0 || cid > 255 || apn == "" || len(apn) > 100 || len(pdpType) > 16 {
			continue
		}
		if _, reserved := reservedAPNLeaves[leaf]; reserved {
			continue
		}
		key := strings.ToLower(apn)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		if pdpType == "" {
			pdpType = "IP"
		}
		result = append(result, ProfileView{
			Name: apn + " (CID " + strconv.Itoa(cid) + ")", APN: apn, Auth: "NONE",
			Source: "modem", PDPType: pdpType, System: true,
		})
	}
	return result
}
