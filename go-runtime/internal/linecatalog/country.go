package linecatalog

import (
	_ "embed"
	"encoding/json"
	"strings"
)

// Direct copy of ec620942 control/app/mcc_country.json; lookup semantics from
// control/app/egress.py:country_for_mcc. This is SIM home country, not roaming.
//
//go:embed mcc_country.json
var mccCountryJSON []byte

var mccCountries = func() map[string]string {
	var values map[string]string
	if err := json.Unmarshal(mccCountryJSON, &values); err != nil {
		panic(err)
	}
	return values
}()

func CountryForMCC(mcc string) string {
	mcc = strings.TrimSpace(mcc)
	if len(mcc) < 3 {
		mcc = strings.Repeat("0", 3-len(mcc)) + mcc
	}
	return mccCountries[mcc]
}
