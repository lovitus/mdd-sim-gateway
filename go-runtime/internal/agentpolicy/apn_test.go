package agentpolicy

import "testing"

func TestParsePDPContextsReturnsOnlyDistinctInternetCandidates(t *testing.T) {
	profiles := ParsePDPContexts([]byte("AT+CGDCONT?\r\n" +
		"+CGDCONT: 1,\"IPV4V6\",\"internet\",\"0.0.0.0\"\r\n" +
		"+CGDCONT: 2,\"IP\",\"ims\",\"0.0.0.0\"\r\n" +
		"+CGDCONT: 3,\"IP\",\"INTERNET\",\"0.0.0.0\"\r\n" +
		"+CGDCONT: 4,\"IP\",\"carrier.data\",\"0.0.0.0\"\r\nOK\r\n"))
	if len(profiles) != 2 || profiles[0].APN != "internet" || profiles[0].PDPType != "IPV4V6" ||
		profiles[0].Source != "modem" || profiles[1].APN != "carrier.data" {
		t.Fatalf("profiles=%+v", profiles)
	}
}
