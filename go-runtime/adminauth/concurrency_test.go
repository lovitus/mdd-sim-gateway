package adminauth

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPasswordChangeRejectsLoginUsingOldSnapshot(t *testing.T) {
	manager := testManager(t, false, time.Now)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	derive := manager.derivePassword
	manager.derivePassword = func(password, salt []byte) ([]byte, error) {
		close(entered)
		<-release
		return derive(password, salt)
	}
	done := make(chan error, 1)
	go func() { _, err := manager.Login("fanli", testPassword, "peer"); done <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("login did not capture credentials")
	}
	if err := manager.ChangePassword(testPassword, "new password"); err != nil {
		t.Fatal(err)
	}
	unblock()
	if err := <-done; !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("stale login err=%v", err)
	}
	if len(manager.sessions) != 0 {
		t.Fatal("old-password login created a session after password replacement")
	}
}

func TestLoginBoundsConcurrentDerivationsBeforeScrypt(t *testing.T) {
	manager := testManager(t, false, time.Now)
	entered := make(chan struct{}, maxLoginDerivations+1)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	manager.derivePassword = func(_, _ []byte) ([]byte, error) {
		entered <- struct{}{}
		<-release
		return nil, nil
	}
	done := make(chan error, maxLoginDerivations)
	for i := 0; i < maxLoginDerivations; i++ {
		go func(peer string) { _, err := manager.Login("fanli", "wrong", peer); done <- err }(fmt.Sprint(i))
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("derivation did not enter")
		}
	}
	for _, peer := range []string{"0", "another-peer"} {
		rejected := make(chan error, 1)
		go func() { _, err := manager.Login("fanli", "wrong", peer); rejected <- err }()
		select {
		case err := <-rejected:
			var throttle *ThrottleError
			if !errors.As(err, &throttle) {
				t.Fatalf("overload err=%v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("overload did not reject promptly")
		}
	}
	unblock()
	for i := 0; i < maxLoginDerivations; i++ {
		if err := <-done; !errors.Is(err, ErrInvalidCredentials) {
			t.Fatal(err)
		}
	}
	if len(manager.loginInFlight) != 0 {
		t.Fatal("derivation capacity leaked")
	}
}

func TestMalformedPasswordDoesNotDerive(t *testing.T) {
	manager := testManager(t, false, time.Now)
	manager.derivePassword = func(_, _ []byte) ([]byte, error) { t.Fatal("unexpected derivation"); return nil, nil }
	for _, password := range []string{"", strings.Repeat("a", 257), string([]byte{0xff})} {
		if _, err := manager.Login("fanli", password, "peer"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("malformed login err=%v", err)
		}
	}
}
