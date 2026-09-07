package service

import (
	"strings"
	"testing"

	"github.com/boa-z/vowifi-go/runtimehost/identity"
)

func TestUpstreamRekeyPeriodBounds(t *testing.T) {
	base := UpstreamConfig{LineID: "line-1", DeviceID: "device-1", Profile: identity.Profile{IMSI: "234100000000001"}, BrokerURL: "http://127.0.0.1:39002/v1/agent/aka", BrokerToken: strings.Repeat("a", 32)}
	for _, minutes := range []int{0, 1, 30, 1440} {
		base.RekeyMinutes = minutes
		factory, err := NewUpstreamFactory(base)
		if err != nil || factory.config.RekeyMinutes != minutes {
			t.Fatalf("period=%d error=%v", minutes, err)
		}
	}
	for _, minutes := range []int{-1, 1441} {
		base.RekeyMinutes = minutes
		if _, err := NewUpstreamFactory(base); err == nil {
			t.Fatalf("accepted period %d", minutes)
		}
	}
}
