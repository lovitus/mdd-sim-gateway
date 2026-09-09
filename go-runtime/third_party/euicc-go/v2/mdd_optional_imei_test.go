package sgp22

import (
	"bytes"
	"github.com/damonto/euicc-go/bertlv"
	"testing"
)

func TestMDDTACOnlyDeviceInfo(t *testing.T) {
	for _, value := range []string{"", "123456789012347"} {
		imei, err := NewIMEI(value)
		if err != nil {
			t.Fatal(err)
		}
		dummy := func() *bertlv.TLV { return bertlv.NewValue(bertlv.Universal.Primitive(4), []byte{1}) }
		r := AuthenticateServerRequest{IMEI: imei, Signed1: dummy(), Signature1: dummy(), UsedIssuer: dummy(), Certificate: dummy()}
		encoded, err := r.MarshalBERTLV()
		if err != nil {
			t.Fatal(err)
		}
		device := encoded.First(bertlv.ContextSpecific.Constructed(0)).First(bertlv.ContextSpecific.Constructed(1))
		identity := device.First(bertlv.ContextSpecific.Primitive(2))
		if value == "" {
			if identity != nil {
				t.Fatal("absent IMEI was encoded")
			}
			if !bytes.Equal(device.First(bertlv.ContextSpecific.Primitive(0)).Value, []byte{0x35, 0x29, 0x06, 0x11}) {
				t.Fatal("default TAC changed")
			}
		} else if identity == nil || !bytes.Equal(identity.Value, imei) {
			t.Fatal("explicit IMEI changed")
		}
	}
}
