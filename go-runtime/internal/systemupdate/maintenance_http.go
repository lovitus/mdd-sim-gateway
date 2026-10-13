package systemupdate

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"
)

const MaintenancePath = "/v1/system/update/maintenance"

type MaintenanceCommand struct {
	Action      string       `json:"action"`
	OperationID string       `json:"operation_id"`
	Revision    uint64       `json:"revision,omitempty"`
	Target      UpdateTarget `json:"target,omitempty"`
	Unit        string       `json:"unit,omitempty"`
}

func (owner *MaintenanceOwner) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path != MaintenancePath || r.URL.RawQuery != "" {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodGet {
		_ = json.NewEncoder(w).Encode(owner.View())
		return
	}
	var command MaintenanceCommand
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	decoder.DisallowUnknownFields()
	if r.Method != http.MethodPost || decoder.Decode(&command) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	// Browser/worker disconnect cannot drop a partly acquired cleanup owner.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Second)
	defer cancel()
	var view MaintenanceView
	var err error
	switch command.Action {
	case "begin":
		view, err = owner.Begin(ctx, command.OperationID, command.Target)
	case "activate":
		view, err = owner.Activate(command.OperationID, command.Revision)
	case "restarted":
		view, err = owner.Restarted(command.OperationID, command.Revision, command.Unit)
	case "finish":
		view, err = owner.Finish(ctx, command.OperationID, command.Revision)
	case "cleanup":
		view, err = owner.Cleanup(ctx, command.OperationID, command.Revision)
	default:
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if err != nil {
		w.WriteHeader(http.StatusConflict)
	}
	_ = json.NewEncoder(w).Encode(struct {
		MaintenanceView
		Error string `json:"error,omitempty"`
	}{view, errorText(err)})
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
