package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/internal/agentcontrol"
)

const localManagementVersion = 1

// This is a whitelist, not a redacted copy of config. Secrets never travel back.
type managedConnection struct {
	AgentID              string `json:"agent_id"`
	Server               string `json:"server"`
	TLSMode              string `json:"tls_mode"`
	Fingerprint          string `json:"fingerprint"`
	CredentialConfigured bool   `json:"credential_configured"`
}

func publicConnection(settings config) managedConnection {
	mode := settings.Agent.TLSMode
	if settings.Agent.TLSFingerprint != "" {
		mode = "pin"
	} else if mode == "" {
		mode = "system"
	}
	return managedConnection{settings.Agent.ID, settings.Agent.ServerURL, mode, settings.Agent.TLSFingerprint, settings.Agent.ServerToken != ""}
}

func (view managedConnection) guiConfig() config {
	var settings config
	settings.Agent.ID, settings.Agent.ServerURL = view.AgentID, view.Server
	settings.Agent.TLSMode, settings.Agent.TLSFingerprint = view.TLSMode, view.Fingerprint
	settings.OperationTimeoutSeconds = 45
	return settings
}

type localManagementState struct {
	Version             int                         `json:"version"`
	Session             string                      `json:"session"`
	Revision            string                      `json:"revision"`
	Running             managedConnection           `json:"running"`
	Saved               managedConnection           `json:"saved"`
	Loaded              bool                        `json:"loaded"`
	Runtime             *agentcontrol.Snapshot      `json:"runtime,omitempty"`
	Topology            *agentlink.TopologySnapshot `json:"topology,omitempty"`
	RuntimeUnavailable  bool                        `json:"runtime_unavailable,omitempty"`
	TopologyUnavailable bool                        `json:"topology_unavailable,omitempty"`
}

type localManagementEdit struct {
	Version  int              `json:"version"`
	Session  string           `json:"session"`
	Revision string           `json:"revision"`
	Change   connectionChange `json:"change"`
}

type localManagementHandler struct {
	settings  config
	session   string
	authorize func() error
	slots     chan struct{}
}

func (handler *localManagementHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	select {
	case handler.slots <- struct{}{}:
		defer func() { <-handler.slots }()
	default:
		managementError(w, http.StatusTooManyRequests, "management_busy")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	if r.URL.RawQuery != "" || r.URL.Path != "/v1/connection" || (r.Method != http.MethodGet && r.Method != http.MethodPut) {
		managementError(w, http.StatusNotFound, "management_operation_unsupported")
		return
	}
	if handler.authorize() != nil {
		managementError(w, http.StatusForbidden, "management_authorization_changed")
		return
	}
	edit, err := readConnectionEdit(handler.settings.configPath)
	if err != nil {
		managementError(w, http.StatusConflict, "management_configuration_unavailable")
		return
	}
	if r.Method == http.MethodPut {
		var request localManagementEdit
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&request) != nil || decoder.Decode(&struct{}{}) != io.EOF ||
			request.Version != localManagementVersion || request.Session != handler.session || request.Revision != edit.digest {
			managementError(w, http.StatusConflict, "management_edit_stale_or_invalid")
			return
		}
		credentialRejected := false
		err = edit.saveGuarded(ctx, request.Change, func(next config) error {
			if err := handler.authorize(); err != nil {
				return &connectionEditFailure{"management_authorization_changed", err}
			}
			if err := checkRetainedConnectionCredential(edit.settings, next, request.Change.Token); err != nil {
				credentialRejected = true
				return err
			}
			return ctx.Err()
		})
		request.Change.Token = ""
		if err != nil {
			code := "management_verification_or_save_failed"
			var failure *connectionEditFailure
			if errors.As(err, &failure) {
				code = failure.code
			}
			if credentialRejected {
				code = "management_new_target_token_required"
			}
			managementError(w, http.StatusConflict, code)
			return
		}
		edit, err = readConnectionEdit(handler.settings.configPath)
		if err != nil {
			managementError(w, http.StatusConflict, "management_saved_readback_unknown")
			return
		}
	}
	state := localManagementState{
		Version: localManagementVersion, Session: handler.session, Revision: edit.digest,
		Running: publicConnection(handler.settings), Saved: publicConnection(edit.settings),
		Loaded: publicConnection(handler.settings) == publicConnection(edit.settings) && handler.settings.Agent.ServerToken == edit.settings.Agent.ServerToken,
	}
	var output bytes.Buffer
	if runClientContext(ctx, "status", handler.settings, &output) == nil {
		var snapshot agentcontrol.Snapshot
		if json.Unmarshal(output.Bytes(), &snapshot) == nil {
			snapshot.Detail, snapshot.RuntimeConfig = "", nil
			state.Runtime = &snapshot
		}
	}
	state.RuntimeUnavailable = state.Runtime == nil
	output.Reset()
	if runClientContext(ctx, "topology", handler.settings, &output) == nil {
		var topology agentlink.TopologySnapshot
		if json.Unmarshal(output.Bytes(), &topology) == nil && topology.Validate() == nil {
			// Preserve typed states, not free-form transport/driver diagnostics.
			topology.ReaderDetail, topology.ModemDetail = "", ""
			for i := range topology.Readers {
				topology.Readers[i].IdentityDetail = ""
			}
			for i := range topology.Modems {
				topology.Modems[i].Detail = ""
				topology.Modems[i].AT.Detail = ""
				topology.Modems[i].Network.DataGuardDetail = ""
			}
			state.Topology = &topology
		}
	}
	state.TopologyUnavailable = state.Topology == nil
	if handler.authorize() != nil {
		managementError(w, http.StatusForbidden, "management_authorization_changed")
		return
	}
	_ = json.NewEncoder(w).Encode(state)
}

func managementError(w http.ResponseWriter, status int, code string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code})
}

func managementMessage(code string) error {
	message := map[string]string{
		"management_new_target_token_required":   "更换 Core 地址或证书信任时必须填写新目标的 Agent token；不会发送原凭据。",
		"management_edit_stale_or_invalid":       "服务或配置已变化，请重新打开编辑器；未重发保存。",
		"management_configuration_unavailable":   "原服务配置暂不可读取或尚有未完成配置，请由管理员检查；不会新建身份。",
		"management_authorization_changed":       "本机管理授权已撤销或变化；请联系管理员。",
		"management_busy":                        "已有管理请求，请稍后手动重试。",
		"management_saved_readback_unknown":      "配置已保存但读回未确认；请刷新，不要重复保存。",
		"management_verification_or_save_failed": "验证或保存未确认：请检查目标、证书和凭据，刷新读取当前配置；没有自动重试。",
		"management_config_locked":               "另一个配置操作正在使用原文件；本次没有写入。",
		"management_input_invalid":               "连接参数无效；请核对地址、证书模式和凭据格式。",
		"management_target_verification_failed":  "目标连通或证书验证失败；未发送凭据、未保存配置。",
		"management_credential_check_failed":     "目标已通过证书验证，但凭据检查未成功；未保存配置。",
		"management_publish_unconfirmed":         "配置文件发布未确认；请刷新读回，不要重复保存。",
	}[code]
	if message == "" {
		message = "本机管理响应未确认；请刷新读取当前状态，不要重复保存。"
	}
	return errors.New(strings.TrimSpace(message))
}
