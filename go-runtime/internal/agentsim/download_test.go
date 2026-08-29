package agentsim

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentreader"
)

func TestEUICCDownloadRoutesSecondSecureElementByEID(t *testing.T) {
	secondEID := "89049032000000000000000000000002"
	card := estkDualCard(t, testEID, secondEID)
	manager, _ := NewManager(fakeConnector{cards: map[string]*fakeCard{"reader": card}}, nil)
	called := make(chan []byte, 1)
	manager.downloadProfile = func(_ context.Context, _ Card, _ agentlink.EUICCDownloadRequest, aid []byte,
		_ func(agentlink.EUICCDownloadStage), _ func(*agentlink.EUICCDownloadMetadata)) error {
		called <- append([]byte(nil), aid...)
		return errors.New("bounded test stop")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- manager.Run(ctx, agentreader.Reader{Name: "reader", CardPresent: true, SessionGeneration: "insertion-1"})
	}()
	waitForSession(t, manager, "insertion-1")
	views := manager.Sessions()
	if len(views) != 1 || views[0].EUICC != nil || len(views[0].SecureElements) != 2 {
		t.Fatalf("dual-SE view=%+v", views)
	}
	request := downloadStartRequestForTest("download-se1")
	request.EID = secondEID
	started := manager.ExecuteEUICCDownload(context.Background(), request)
	if started.Failure != nil {
		t.Fatalf("start=%+v", started)
	}
	terminal := waitForDownload(t, manager, request)
	if terminal.State != agentlink.EUICCDownloadFailed {
		t.Fatalf("terminal=%+v", terminal)
	}
	select {
	case aid := <-called:
		if !bytes.Equal(aid, estkSE1AID) {
			t.Fatalf("download AID=%X want %X", aid, estkSE1AID)
		}
	default:
		t.Fatal("second secure element was not routed")
	}
	cancel()
	<-done
}

func TestEUICCDownloadCompletesOnceAndRefreshesExactSession(t *testing.T) {
	card := euiccCard(t, emptyProfileResponse())
	store, err := OpenDownloadStore(t.TempDir()+"/downloads.db", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	manager, err := NewManagerWithDownloadStore(fakeConnector{cards: map[string]*fakeCard{"reader": card}}, nil, store)
	if err != nil {
		t.Fatal(err)
	}
	runs := 0
	manager.downloadProfile = func(_ context.Context, got Card, request agentlink.EUICCDownloadRequest, _ []byte,
		progress func(agentlink.EUICCDownloadStage), metadata func(*agentlink.EUICCDownloadMetadata)) error {
		runs++
		if got != card || request.EID != testEID {
			t.Fatalf("download target card=%p request=%+v", got, request)
		}
		progress(agentlink.EUICCDownloadStageAuthenticateServer)
		metadata(&agentlink.EUICCDownloadMetadata{ICCID: "8944000000000000001", ProfileName: "test"})
		progress(agentlink.EUICCDownloadStageInstall)
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- manager.Run(ctx, agentreader.Reader{Name: "reader", CardPresent: true, SessionGeneration: "insertion-1"})
	}()
	waitForSession(t, manager, "insertion-1")
	request := downloadStartRequestForTest("download-1")
	started := manager.ExecuteEUICCDownload(context.Background(), request)
	if started.Failure != nil || started.Job == nil || started.Job.State != agentlink.EUICCDownloadQueued {
		t.Fatalf("start=%+v", started)
	}
	terminal := waitForStoredDownload(t, manager, request.OperationID)
	if terminal.State != agentlink.EUICCDownloadCompleted || terminal.Stage != agentlink.EUICCDownloadStageCompleted ||
		terminal.Metadata == nil || terminal.Metadata.ICCID != "8944000000000000001" || runs != 1 {
		t.Fatalf("terminal=%+v runs=%d", terminal, runs)
	}
	select {
	case runErr := <-done:
		if !errors.Is(runErr, errEUICCProfileChanged) {
			t.Fatalf("refresh error=%v", runErr)
		}
	case <-time.After(time.Second):
		t.Fatal("completed download did not refresh its card session")
	}
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- manager.Run(ctx, agentreader.Reader{Name: "reader", CardPresent: true, SessionGeneration: "insertion-2"})
	}()
	waitForSession(t, manager, "insertion-2")
	request.SessionGeneration = "insertion-2"
	replayed := manager.ExecuteEUICCDownload(context.Background(), request)
	if replayed.Failure != nil || replayed.Job == nil || replayed.Job.State != agentlink.EUICCDownloadCompleted || runs != 1 {
		t.Fatalf("replay=%+v runs=%d", replayed, runs)
	}
	cancel()
	<-secondDone
}

func TestEUICCDownloadFailureBeforeInstallKeepsSessionAndCancelIsBounded(t *testing.T) {
	card := euiccCard(t, emptyProfileResponse())
	manager, _ := NewManager(fakeConnector{cards: map[string]*fakeCard{"reader": card}}, nil)
	manager.downloadProfile = func(ctx context.Context, _ Card, request agentlink.EUICCDownloadRequest, _ []byte,
		_ func(agentlink.EUICCDownloadStage), _ func(*agentlink.EUICCDownloadMetadata)) error {
		if request.OperationID == "download-failed" {
			return errors.New("authenticate client failed")
		}
		<-ctx.Done()
		return ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- manager.Run(ctx, agentreader.Reader{Name: "reader", CardPresent: true, SessionGeneration: "insertion-1"})
	}()
	waitForSession(t, manager, "insertion-1")
	failedRequest := downloadStartRequestForTest("download-failed")
	manager.ExecuteEUICCDownload(context.Background(), failedRequest)
	failed := waitForDownload(t, manager, failedRequest)
	if failed.State != agentlink.EUICCDownloadFailed || failed.Stage != agentlink.EUICCDownloadStageAuthenticateClient {
		t.Fatalf("failed=%+v", failed)
	}
	select {
	case runErr := <-done:
		t.Fatalf("pre-install failure refreshed session: %v", runErr)
	case <-time.After(20 * time.Millisecond):
	}
	cancelRequest := downloadStartRequestForTest("download-cancel")
	manager.ExecuteEUICCDownload(context.Background(), cancelRequest)
	statusRequest := cancelRequest
	statusRequest.Action, statusRequest.ActivationCode, statusRequest.IMEI = agentlink.EUICCDownloadStatus, "", ""
	deadline := time.Now().Add(time.Second)
	for {
		status := manager.ExecuteEUICCDownload(context.Background(), statusRequest)
		if status.Job != nil && status.Job.State == agentlink.EUICCDownloadRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("download did not start: %+v", status)
		}
		time.Sleep(time.Millisecond)
	}
	cancelCommand := statusRequest
	cancelCommand.Action = agentlink.EUICCDownloadCancel
	cancelled := manager.ExecuteEUICCDownload(context.Background(), cancelCommand)
	if cancelled.Failure != nil || cancelled.Job == nil || cancelled.Job.State != agentlink.EUICCDownloadCancelling {
		t.Fatalf("cancel=%+v", cancelled)
	}
	terminal := waitForDownload(t, manager, cancelRequest)
	if terminal.State != agentlink.EUICCDownloadCanceled || terminal.Code != "download_canceled" {
		t.Fatalf("cancel terminal=%+v", terminal)
	}
	cancel()
	<-done
}

func downloadStartRequestForTest(operationID string) agentlink.EUICCDownloadRequest {
	return agentlink.EUICCDownloadRequest{
		OperationID: operationID, SessionGeneration: "insertion-1", EID: testEID,
		Action: agentlink.EUICCDownloadStart, ActivationCode: "LPA:1$example.com$matching-id",
		IMEI: "123456789012345",
	}
}

func waitForDownload(t *testing.T, manager *Manager, request agentlink.EUICCDownloadRequest) agentlink.EUICCDownloadJob {
	t.Helper()
	request.Action, request.ActivationCode, request.ConfirmationCode, request.IMEI = agentlink.EUICCDownloadStatus, "", "", ""
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		result := manager.ExecuteEUICCDownload(context.Background(), request)
		if result.Failure != nil {
			t.Fatalf("status failure=%+v", result)
		}
		if result.Job != nil && downloadTerminal(result.Job.State) {
			return *result.Job
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("download did not reach a terminal state")
	return agentlink.EUICCDownloadJob{}
}

func waitForStoredDownload(t *testing.T, manager *Manager, operationID string) agentlink.EUICCDownloadJob {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		manager.downloadMu.Lock()
		job := manager.downloads[operationID]
		manager.downloadMu.Unlock()
		if job != nil {
			status := job.snapshot()
			if downloadTerminal(status.State) {
				return status
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("stored download did not reach a terminal state")
	return agentlink.EUICCDownloadJob{}
}
