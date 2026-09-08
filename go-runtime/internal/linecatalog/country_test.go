package linecatalog

import "testing"

func TestOriginalMCCHomeCountryMapping(t *testing.T) {
	for mcc, want := range map[string]string{"234": "gb", "208": "fr", "454": "hk", "460": "cn", "999": "", "": ""} {
		if got := CountryForMCC(mcc); got != want {
			t.Fatalf("MCC %s: %s, want %s", mcc, got, want)
		}
	}
}
