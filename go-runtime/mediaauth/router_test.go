package mediaauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/mediaproxy"
)

const testProviderToken = "0123456789abcdef0123456789abcdef"

func TestRouterBindsSubjectAndCurrentProviderGeneration(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	providers := NewProviderDirectory()
	if err := providers.Replace(Provider{LineID: "line-1", ProviderID: "vowifi-1", Generation: "provider-1", BaseURL: "ws://127.0.0.1:9000", Token: testProviderToken}); err != nil {
		t.Fatal(err)
	}
	verifier := SessionVerifierFunc(func(_ context.Context, request *http.Request) (string, error) {
		cookie, err := request.Cookie("mdd_session")
		if err != nil || cookie.Value != "valid" {
			return "", errors.New("invalid session")
		}
		return "subject-1", nil
	})
	router, err := NewRouter(verifier, providers, func() time.Time { return now }, 2)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := router.Issue(LeaseRequest{Subject: "subject-1", LineID: "line-1", CallID: "call-1", ProviderGeneration: "provider-1", ExpiresAt: now.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/browser-media/"+lease.SessionID+"/ws", nil)
	request.SetPathValue("sessionID", lease.SessionID)
	request.AddCookie(&http.Cookie{Name: "mdd_session", Value: "valid"})
	target, err := router.AuthorizeMedia(context.Background(), request)
	if err != nil || target.URL != "ws://127.0.0.1:9000/v1/media/"+lease.SessionID || target.Token != testProviderToken {
		t.Fatalf("target=%+v err=%v", target, err)
	}
	if err := providers.Replace(Provider{LineID: "line-1", ProviderID: "vowifi-1", Generation: "provider-2", BaseURL: "ws://127.0.0.1:9001", Token: testProviderToken}); err != nil {
		t.Fatal(err)
	}
	_, err = router.AuthorizeMedia(context.Background(), request)
	assertAuthorization(t, err, http.StatusConflict, "media_provider_changed")
}

func TestRouterRejectsMissingOwnerExpiredAndRevokedLeases(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	providers := NewProviderDirectory()
	_ = providers.Replace(Provider{LineID: "line-1", ProviderID: "vowifi-1", Generation: "provider-1", BaseURL: "ws://127.0.0.1:9000", Token: testProviderToken})
	verifier := SessionVerifierFunc(func(_ context.Context, request *http.Request) (string, error) {
		cookie, err := request.Cookie("mdd_session")
		if err != nil {
			return "", err
		}
		return cookie.Value, nil
	})
	router, _ := NewRouter(verifier, providers, func() time.Time { return now }, 1)
	lease, _ := router.Issue(LeaseRequest{Subject: "owner", LineID: "line-1", CallID: "call-1", ProviderGeneration: "provider-1", ExpiresAt: now.Add(time.Minute)})
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.SetPathValue("sessionID", lease.SessionID)
	_, err := router.AuthorizeMedia(context.Background(), request)
	assertAuthorization(t, err, http.StatusUnauthorized, "login_required")
	request.AddCookie(&http.Cookie{Name: "mdd_session", Value: "other"})
	_, err = router.AuthorizeMedia(context.Background(), request)
	assertAuthorization(t, err, http.StatusForbidden, "media_lease_owner_mismatch")

	now = now.Add(2 * time.Minute)
	request.Header.Set("Cookie", "mdd_session=owner")
	_, err = router.AuthorizeMedia(context.Background(), request)
	assertAuthorization(t, err, http.StatusGone, "media_lease_expired")

	second, err := router.Issue(LeaseRequest{Subject: "owner", LineID: "line-1", CallID: "call-2", ProviderGeneration: "provider-1", ExpiresAt: now.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	router.Revoke(second.SessionID)
	request.SetPathValue("sessionID", second.SessionID)
	_, err = router.AuthorizeMedia(context.Background(), request)
	assertAuthorization(t, err, http.StatusGone, "media_lease_expired")
}

func TestProviderDirectoryRejectsRemoteAndLateRemoval(t *testing.T) {
	directory := NewProviderDirectory()
	if err := directory.Replace(Provider{LineID: "line", ProviderID: "vowifi-1", Generation: "one", BaseURL: "ws://192.0.2.1:9000", Token: testProviderToken}); err == nil {
		t.Fatal("remote provider was accepted")
	}
	if err := directory.Replace(Provider{LineID: "line", ProviderID: "vowifi-1", Generation: "two", BaseURL: "ws://127.0.0.1:9000", Token: testProviderToken}); err != nil {
		t.Fatal(err)
	}
	if err := directory.Replace(Provider{LineID: "line", ProviderID: "vowifi-1", Generation: "two", BaseURL: "ws://127.0.0.1:9000", Token: testProviderToken}); err != nil {
		t.Fatalf("idempotent registration failed: %v", err)
	}
	directory.Remove("line", "one")
	if _, err := directory.ResolveMedia(context.Background(), "line", "two", "session"); err != nil {
		t.Fatal("late removal deleted replacement")
	}
	if err := directory.Replace(Provider{LineID: "line", ProviderID: "vowifi-1", Generation: "three", BaseURL: "ws://127.0.0.1:9001", Token: testProviderToken}); err != nil {
		t.Fatal(err)
	}
	if err := directory.Replace(Provider{LineID: "line", ProviderID: "vowifi-1", Generation: "two", BaseURL: "ws://127.0.0.1:9000", Token: testProviderToken}); !errors.Is(err, ErrProviderGenerationReused) {
		t.Fatalf("old generation replacement error=%v", err)
	}
}

func assertAuthorization(t *testing.T, err error, status int, code string) {
	t.Helper()
	var got *mediaproxy.AuthorizationError
	if !errors.As(err, &got) || got.Status != status || got.Code != code {
		t.Fatalf("authorization=%+v err=%v", got, err)
	}
}
