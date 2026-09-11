package agentsim

import (
	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"testing"
)

func TestRestartPreflightRejectsCardOperationsAndUnfinishedDownloads(t *testing.T) {
	card := &session{}
	manager := &Manager{sessions: map[string]*session{"session": card}}
	card.operation.Lock()
	if manager.CheckRestartSafe() == nil {
		t.Fatal("active card operation allowed restart")
	}
	card.operation.Unlock()
	if err := manager.CheckRestartSafe(); err != nil {
		t.Fatal(err)
	}
	job := &downloadJob{}
	manager.downloads = map[string]*downloadJob{"download": job}
	for _, state := range []agentlink.EUICCDownloadState{agentlink.EUICCDownloadQueued, agentlink.EUICCDownloadRunning, agentlink.EUICCDownloadCancelling, agentlink.EUICCDownloadUncertain} {
		job.status.State = state
		if manager.CheckRestartSafe() == nil {
			t.Fatalf("%s download allowed restart", state)
		}
	}
	job.status.State = agentlink.EUICCDownloadCompleted
	if err := manager.CheckRestartSafe(); err != nil {
		t.Fatal(err)
	}
	manager.refreshExpected = map[string]string{"reader": "session"}
	if manager.CheckRestartSafe() == nil {
		t.Fatal("pending reader refresh allowed restart")
	}
}
