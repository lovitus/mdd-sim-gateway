package linecatalog

import (
	"path/filepath"
	"testing"
	"time"
)

func TestLinePersistsTypedIMSNetworkModesAndIMEISV(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "catalog.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	line := Line{SchemaVersion: SchemaVersion, ID: "line-1", CardID: "8944100000000000001",
		SIM:     SIMConfig{IMEISV: "3567890123456789"},
		Network: NetworkConfig{IMSAPN: " IMS-CUSTOM ", IDRMode: " FQDN ", CPMode: " AUTO "}}
	stored, err := store.Put(line)
	if err != nil {
		t.Fatal(err)
	}
	if stored.SIM.IMEISV != "3567890123456789" || stored.Network.IMSAPN != "ims-custom" ||
		stored.Network.IDRMode != "fqdn" || stored.Network.CPMode != "auto" {
		t.Fatalf("stored=%+v", stored)
	}
	for name, mutate := range map[string]func(*Line){
		"imeisv":  func(value *Line) { value.SIM.IMEISV = "123" },
		"ims apn": func(value *Line) { value.Network.IMSAPN = "bad\nvalue" },
		"idr":     func(value *Line) { value.Network.IDRMode = "realm" },
		"cp":      func(value *Line) { value.Network.CPMode = "ipv5" },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := line
			invalid.ID = "invalid-line"
			mutate(&invalid)
			if _, err := store.Put(invalid); err == nil {
				t.Fatal("invalid network identity was accepted")
			}
		})
	}
}
