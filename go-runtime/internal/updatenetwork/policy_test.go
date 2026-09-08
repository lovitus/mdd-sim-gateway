package updatenetwork

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestPrivatePolicyCheckNeverFollowsRedirect(t *testing.T) {
	var redirected atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/other" {
			redirected.Add(1)
			return
		}
		http.Redirect(w, r, "/other", http.StatusFound)
	}))
	defer server.Close()
	if err := CheckPolicy(t.Context(), server.URL, strings.Repeat("t", 32), 1, server.Client()); err == nil {
		t.Fatal("redirect accepted as policy confirmation")
	}
	if redirected.Load() != 0 {
		t.Fatal("local token followed a redirect")
	}
}
