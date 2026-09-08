package agentcontrol

import "testing"

func TestRuntimeConfigIdentityIsCanonicalAndCopied(t *testing.T) {
	a, err := ModemRuntimeConfig("serial", []byte(`[{"vid":"2c7c","pid":"0125"}]`), true, false)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ModemRuntimeConfig("serial", []byte(`[{"pid":"0125", "vid":"2c7c"}]`), true, false)
	if err != nil || a != b {
		t.Fatal("profile formatting changed identity", err)
	}
	controller, err := New(&fakeWorker{}, nil, a)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := controller.Status()
	snapshot.RuntimeConfig.ModemBackend = "auto"
	if controller.Status().RuntimeConfig.ModemBackend != "serial" {
		t.Fatal("caller changed loaded configuration identity")
	}
	if a.SIMAPDUEnabled {
		t.Fatal("identity construction enabled APDU")
	}
}
