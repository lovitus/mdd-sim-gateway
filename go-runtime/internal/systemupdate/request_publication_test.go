package systemupdate

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestUpdateWorkerPublicationWaitsForOriginalCoreStore(t *testing.T) {
	owner, _, store := updateTestOwner(t, 0)
	if err := store.SetStatus(Status{OperationID: "update-one", Target: "2.0.0", State: StateFailed, UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	published, resume := make(chan struct{}), make(chan struct{})
	store.rename = func(from, to string) error {
		if err := os.Rename(from, to); err != nil {
			return err
		}
		if to == store.requestPath {
			close(published)
			<-resume
		}
		return nil
	}
	writeDone := make(chan error, 1)
	go func() {
		writeDone <- store.Request(Request{OperationID: "update-two", Repository: "example/project", Target: "3.0.0", RequestedAt: time.Now()})
	}()
	<-published
	worker, err := Open(strings.TrimSuffix(store.requestPath, ".request.json"))
	if err != nil {
		close(resume)
		t.Fatal(err)
	}
	request, found, err := worker.PendingRequest()
	if err != nil || !found || request.OperationID != "update-two" {
		close(resume)
		t.Fatalf("trigger request not published: %+v %v", request, err)
	}
	entered := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		owner.ServeHTTP(w, r)
	}))
	defer server.Close()
	result := make(chan MaintenanceView, 1)
	errorsSeen := make(chan error, 1)
	go func() {
		payload, _ := json.Marshal(MaintenanceCommand{Action: "request", OperationID: request.OperationID, RequestedTarget: request.Target})
		response, err := server.Client().Post(server.URL+MaintenancePath, "application/json", bytes.NewReader(payload))
		if err != nil {
			errorsSeen <- err
			return
		}
		defer response.Body.Close()
		var view MaintenanceView
		if err := json.NewDecoder(response.Body).Decode(&view); err != nil {
			errorsSeen <- err
			return
		}
		result <- view
	}()
	<-entered
	select {
	case view := <-result:
		close(resume)
		<-writeDone
		t.Fatalf("REQUEST_PUBLICATION: worker escaped incomplete publication: %+v", view.Publication)
	case err := <-errorsSeen:
		close(resume)
		<-writeDone
		t.Fatal(err)
	case <-time.After(200 * time.Millisecond):
	}
	close(resume)
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}
	select {
	case view := <-result:
		if view.Publication == nil || !view.Publication.Committed || view.Publication.Request.OperationID != request.OperationID || view.Publication.Status.State != StateRequested {
			t.Fatalf("REQUEST_PUBLICATION: committed original request was lost: %+v", view.Publication)
		}
	case err := <-errorsSeen:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("worker publication read did not finish")
	}
}

func TestUpdatePublicationFailureCannotBecomeReadyFromMatchingFiles(t *testing.T) {
	for _, afterRename := range []bool{false, true} {
		t.Run(map[bool]string{false: "status-rename", true: "status-sync"}[afterRename], func(t *testing.T) {
			owner, _, store := updateTestOwner(t, 0)
			if err := store.SetStatus(Status{OperationID: "update-one", Target: "2.0.0", State: StateFailed, UpdatedAt: time.Now()}); err != nil {
				t.Fatal(err)
			}
			renamedStatus := false
			store.rename = func(from, to string) error {
				if to == store.statusPath && !afterRename {
					return errors.New("status rename unavailable")
				}
				if err := os.Rename(from, to); err != nil {
					return err
				}
				renamedStatus = to == store.statusPath
				return nil
			}
			store.syncDirectory = func(path string) error {
				if renamedStatus {
					return errors.New("status directory sync unavailable")
				}
				return syncUpdateDirectory(path)
			}
			request := Request{OperationID: "update-two", Repository: "example/project", Target: "3.0.0", RequestedAt: time.Now()}
			if err := store.Request(request); err == nil {
				t.Fatal("injected publication failure ignored")
			}
			view, err := owner.Publication(request.OperationID, request.Target)
			if err != nil || view.Publication == nil || view.Publication.Committed || view.Publication.Status.State != StateUnknown {
				t.Fatalf("PUBLICATION_DURABILITY: %+v %v", view.Publication, err)
			}
			if _, err := owner.Publication(request.OperationID, "different-target"); err == nil {
				t.Fatal("publication accepted a different target")
			}
			if err := store.Request(Request{OperationID: "another", Repository: "example/project", Target: "4.0.0", RequestedAt: time.Now()}); err == nil {
				t.Fatal("half-published original request overwritten")
			}
			store.rename, store.syncDirectory = os.Rename, syncUpdateDirectory
			worker, err := Open(strings.TrimSuffix(store.requestPath, ".request.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := worker.SetStatus(Status{OperationID: request.OperationID, Target: request.Target, State: StateFailed, Phase: "cancelled", UpdatedAt: time.Now()}); err != nil {
				t.Fatal(err)
			}
			if err := store.Request(Request{OperationID: "another", Repository: "example/project", Target: "4.0.0", RequestedAt: time.Now()}); err != nil {
				t.Fatal("original cancellation did not restore request admission", err)
			}
		})
	}
}
