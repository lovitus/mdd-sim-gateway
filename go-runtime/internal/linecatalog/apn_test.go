package linecatalog

import "testing"

func TestMDDAPNProfilesValidateAndFenceActiveProfile(t *testing.T) {
	line := Line{SchemaVersion: SchemaVersion, ID: "line-apn", Name: "APN", CardID: "89010000000000000001",
		Network: NetworkConfig{APNProfiles: []APNProfile{{ID: "carrier", Name: "Carrier", APN: "internet", Auth: "NONE"}}, ActiveAPN: "carrier"}}
	if err := line.normalizeAndValidate(); err != nil {
		t.Fatal(err)
	}
	line.Network.ActiveAPN = "missing"
	if err := line.normalizeAndValidate(); err == nil {
		t.Fatal("unknown active APN accepted")
	}
	line.Network.ActiveAPN = "carrier"
	line.Network.APNProfiles[0].Password = "secret"
	if err := line.normalizeAndValidate(); err == nil {
		t.Fatal("password without password_set accepted")
	}
}
