package adminauth

import "testing"

func TestMarshalBootstrapCredentialRejectsUnicodeControlUsername(t *testing.T) {
	for _, username := range []string{"admin\x00name", "admin\u0085name", "admin\rname"} {
		if _, err := MarshalBootstrapCredential(username, []byte("private password"), "0123456789abcdef0123456789abcdef", nil); err == nil {
			t.Fatalf("control-bearing username %q was accepted", username)
		}
	}
}
