package voiceclient

import (
	"strings"
	"testing"
)

func TestDefaultSecurityClientAgreementUsesDistinctDynamicPorts(t *testing.T) {
	agreement := DefaultSecurityClientAgreement(strings.NewReader(
		"\x00\x01\x00\x02\x00\x00\x00\x03\x00\x00\x00\x04",
	))
	if agreement.PortClient != dynamicSecurityPortMin+1 || agreement.PortServer != dynamicSecurityPortMin+2 {
		t.Fatalf("ports=(%d,%d), want (%d,%d)", agreement.PortClient, agreement.PortServer, dynamicSecurityPortMin+1, dynamicSecurityPortMin+2)
	}
	if agreement.PortClient == agreement.PortServer || agreement.PortClient <= 49151 || agreement.PortServer <= 49151 {
		t.Fatalf("ports are not distinct dynamic ports: (%d,%d)", agreement.PortClient, agreement.PortServer)
	}
}

func TestCompleteSecurityClientAgreementPreservesExplicitPorts(t *testing.T) {
	agreement := completeSecurityClientAgreement(SecurityAgreement{
		Algorithm: DefaultSecurityAlgorithm, PortClient: 42000, PortServer: 52000,
	}, strings.NewReader("\x00\x00\x00\x65\x00\x00\x00\x66"))
	if agreement.PortClient != 42000 || agreement.PortServer != 52000 {
		t.Fatalf("explicit ports changed: (%d,%d)", agreement.PortClient, agreement.PortServer)
	}
}

func TestBuildIMSSecurityAssociationPlanForClientCrossesUEAndPCSCFValues(t *testing.T) {
	client := SecurityAgreement{
		Protocol: DefaultSecurityProtocol, Algorithm: DefaultSecurityAlgorithm,
		SPIClient: 1001, SPIServer: 1002, PortClient: 49153, PortServer: 49154,
	}
	server := SecurityAgreement{
		Protocol: DefaultSecurityProtocol, Algorithm: DefaultSecurityAlgorithm,
		SPIClient: 2001, SPIServer: 2002, PortClient: 5062, PortServer: 5063,
		Parameters: map[string]string{"q": "0.8"}, Raw: "selected-server",
	}
	plan, ok := BuildIMSSecurityAssociationPlanForClient(server, client)
	if !ok {
		t.Fatal("BuildIMSSecurityAssociationPlanForClient() ok=false")
	}
	if plan.SPIClient != 1001 || plan.SPIServer != 2002 ||
		plan.PortClient != 49153 || plan.PortServer != 5063 ||
		plan.Inbound.SPI != 1001 || plan.Outbound.SPI != 2002 ||
		plan.QValue != "0.8" || plan.Source != "selected-server" {
		t.Fatalf("plan=%+v", plan)
	}
}

func TestSelectSecurityAgreementPrefersInstallableIPSecSA(t *testing.T) {
	const installable = `IPSEC-3GPP;Q="0.2";ALG="HMAC-SHA-1-96";EALG="NULL";SPI-C="333";SPI-S="444";PORT-C="5064";PORT-S="5065";PROT=ESP;MODE=TRANSPORT`
	cases := []struct {
		name string
		bad  string
	}{
		{
			name: "invalid client port",
			bad:  `ipsec-3gpp;q=1.0;alg=hmac-sha-1-96;ealg=null;spi-c=111;spi-s=222;port-c=70000;port-s=5063`,
		},
		{
			name: "zero client spi",
			bad:  `ipsec-3gpp;q=1.0;alg=hmac-sha-1-96;ealg=null;spi-c=0;spi-s=222;port-c=5062;port-s=5063`,
		},
		{
			name: "oversized server spi",
			bad:  `ipsec-3gpp;q=1.0;alg=hmac-sha-1-96;ealg=null;spi-c=111;spi-s=4294967296;port-c=5062;port-s=5063`,
		},
	}

	client := SecurityAgreement{
		Protocol:            DefaultSecurityProtocol,
		Algorithm:           DefaultSecurityAlgorithm,
		EncryptionAlgorithm: DefaultSecurityEAlg,
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			selected, ok := SelectSecurityAgreement([]string{tc.bad + ", " + installable}, client)
			if !ok {
				t.Fatal("SelectSecurityAgreement() ok=false")
			}
			if selected.SPIClient != 333 || selected.SPIServer != 444 ||
				selected.PortClient != 5064 || selected.PortServer != 5065 ||
				selected.Parameters["q"] != "0.2" ||
				selected.Parameters["mode"] != "TRANSPORT" ||
				selected.Raw == "" {
				t.Fatalf("selected=%+v", selected)
			}
			plan, ok := BuildIMSSecurityAssociationPlan(selected)
			if !ok {
				t.Fatalf("BuildIMSSecurityAssociationPlan(%+v) ok=false", selected)
			}
			if plan.SPIClient != 333 || plan.SPIServer != 444 ||
				plan.PortClient != 5064 || plan.PortServer != 5065 ||
				plan.Mode != "transport" || plan.QValue != "0.2" {
				t.Fatalf("plan=%+v", plan)
			}
		})
	}
}

func TestSelectSecurityAgreementPreservesSelectedRawFormatting(t *testing.T) {
	const selectedRaw = `IPSEC-3GPP;Q="0.7";PORT-S="5063";SPI-S="222";PORT-C="5062";SPI-C="111";EALG="NULL";ALG="HMAC-SHA-1-96";note="v,1;quoted";PROT=ESP;MODE=TRANSPORT`
	selected, ok := SelectSecurityAgreement([]string{
		`ipsec-3gpp;alg=hmac-md5-96;ealg=null;spi-c=333;spi-s=444;port-c=5064;port-s=5065;q=1.0,` + selectedRaw,
	}, SecurityAgreement{
		Protocol:            DefaultSecurityProtocol,
		Algorithm:           DefaultSecurityAlgorithm,
		EncryptionAlgorithm: DefaultSecurityEAlg,
	})
	if !ok {
		t.Fatal("SelectSecurityAgreement() ok=false")
	}
	if selected.Raw != selectedRaw || selected.Parameters["note"] != "v,1;quoted" || selected.SPIClient != 111 || selected.SPIServer != 222 {
		t.Fatalf("selected=%+v, want raw %q", selected, selectedRaw)
	}
	plan, ok := BuildIMSSecurityAssociationPlan(selected)
	if !ok {
		t.Fatalf("BuildIMSSecurityAssociationPlan(%+v) ok=false", selected)
	}
	if plan.Source != selectedRaw || plan.QValue != "0.7" || plan.Mode != "transport" {
		t.Fatalf("plan=%+v, want source %q", plan, selectedRaw)
	}
}
