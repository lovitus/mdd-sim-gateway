//go:build gui && (darwin || windows)

package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentcontrol"
)

func TestGUIActionCancellationAfterPreflight(t *testing.T) {
	for _, cancellable := range []bool{true, false} {
		name := "noncancellable-read"
		if cancellable {
			name = "http-context"
		}
		t.Run(name, func(t *testing.T) {
			current, err := agentcontrol.New(processWorker{}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer current.Stop(context.Background())
			settings, err := newEditableConfig()
			if err != nil {
				t.Fatal(err)
			}
			settings.OperationTimeoutSeconds = 5
			api, err := agentcontrol.NewAPI(current, settings.Control.Token, time.Second, nil)
			if err != nil {
				t.Fatal(err)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			var mutations atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					close(entered)
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
				} else {
					mutations.Add(1)
				}
				api.ServeHTTP(w, r)
			}))
			defer server.Close()
			defer close(release)
			settings.Control.Listen = strings.TrimPrefix(server.URL, "http://")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			readBack := make(chan struct{})
			done := make(chan error, 1)
			go func() {
				done <- runGUIAction(ctx, func(ctx context.Context) error {
					readCtx := ctx
					if !cancellable {
						readCtx = t.Context()
					}
					err := runClientContext(readCtx, "status", settings, &bytes.Buffer{})
					close(readBack)
					return err
				}, func(context.Context) error {
					// Once dispatched, the external control operation has its own
					// outcome; a cancelled invocation must never reach it.
					return runClient("start", settings, &bytes.Buffer{})
				})
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("preflight did not arrive")
			}
			cancel()
			if cancellable {
				select {
				case <-readBack:
				case <-time.After(time.Second):
					t.Fatal("read ignored invocation cancellation")
				}
			} else {
				release <- struct{}{}
			}
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) || mutations.Load() != 0 || current.Status().State != agentcontrol.StateStopped {
					t.Fatalf("cancelled invocation dispatched: err=%v mutations=%d state=%s", err, mutations.Load(), current.Status().State)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("action did not settle")
			}
		})
	}
}

func TestGUIRuntimeOwnerDoesNotAdoptReplacement(t *testing.T) {
	now := func() time.Time { return time.Unix(1700000000, 0) }
	first, err := agentcontrol.New(processWorker{}, now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := agentcontrol.New(processWorker{}, now)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Stop(context.Background())
	defer second.Stop(context.Background())
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if _, err := first.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Start(ctx); err != nil {
		t.Fatal(err)
	}
	observed := first.Status()
	if observed.Generation != second.Status().Generation || observed.State != second.Status().State || observed.ChangedAt != second.Status().ChangedAt {
		t.Fatal("fixture must have identical numeric state and timestamp")
	}
	lifetime, end := context.WithCancel(ctx)
	owner := &guiRuntimeOwner{lifetime: lifetime, runtime: first}
	end()
	if err := owner.apply(ctx, observed, "stop"); err == nil || first.Status().State != agentcontrol.StateRunning || second.Status().State != agentcontrol.StateRunning {
		t.Fatal("retired GUI ownership admitted a mutation")
	}
	owner = &guiRuntimeOwner{lifetime: ctx, runtime: first}
	if err := owner.apply(ctx, observed, "stop"); err != nil {
		t.Fatal(err)
	}
	if first.Status().State != agentcontrol.StateStopped || second.Status().State != agentcontrol.StateRunning {
		t.Fatal("action did not stay with the captured owner")
	}
	if err := (*guiRuntimeOwner)(nil).apply(ctx, observed, "start"); err == nil {
		t.Fatal("unowned host admitted")
	}
}
