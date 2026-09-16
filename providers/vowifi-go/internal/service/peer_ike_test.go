// SPDX-License-Identifier: AGPL-3.0-only

package service

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/boa-z/vowifi-go/engine/swu/ikev2"
	"github.com/boa-z/vowifi-go/runtimehost/identity"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/vowifiipc"
	"github.com/lovitus/mdd-sim-gateway/providers/vowifi-go/internal/outerudp"
)

func peerFixture(t *testing.T) (*peerIKEResponder, ikev2.InitResult) {
	t.Helper()
	profile, err := ikev2.KeyMaterialProfileFromSA(ikev2.DefaultIKEProposal())
	if err != nil {
		t.Fatal(err)
	}
	material := make([]byte, profile.RequiredLength())
	for i := range material {
		material[i] = byte(i)
	}
	keys, err := ikev2.SplitIKEKeys(profile, material)
	if err != nil {
		t.Fatal(err)
	}
	init := ikev2.InitResult{InitiatorSPI: 0x0102030405060708, ResponderSPI: 0x1112131415161718, Keys: keys}
	peer := newPeerIKEResponder(init, identity.Profile{IMEI: "123456789012345"}, nil)
	peer.setChild([]byte{1, 2, 3, 4}, []byte{5, 6, 7, 8})
	return peer, init
}

func peerRequest(t *testing.T, init ikev2.InitResult, id uint32, payloads ...ikev2.Payload) []byte {
	t.Helper()
	_, raw, err := ikev2.BuildInformationalRequestFrom(init, init.Keys, id, false, payloads, nil)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func decodePeerReply(t *testing.T, init ikev2.InitResult, id uint32, reply outerudp.PeerReply) []ikev2.Payload {
	t.Helper()
	_, inner, err := ikev2.ParseInformationalResponseFrom(reply.Packet, init, init.Keys, id, true)
	if err != nil {
		t.Fatal(err)
	}
	return inner
}

func TestPeerDPDUsesAuthenticatedResponseAndExactReplay(t *testing.T) {
	peer, init := peerFixture(t)
	request := peerRequest(t, init, 0)
	reply, err := peer.handle(request)
	if err != nil {
		t.Fatal(err)
	}
	if inner := decodePeerReply(t, init, 0, reply); len(inner) != 0 || reply.Abort != nil {
		t.Fatal("DPD response was not empty and non-terminal")
	}
	duplicate, err := peer.handle(request)
	if err != nil || !bytes.Equal(reply.Packet, duplicate.Packet) {
		t.Fatal("duplicate regenerated encrypted response")
	}
	duplicate.Packet[0] ^= 1
	again, err := peer.handle(request)
	if err != nil || !bytes.Equal(reply.Packet, again.Packet) {
		t.Fatal("caller mutated cached response")
	}
	for _, mutate := range []func([]byte){
		func(p []byte) { p[len(p)-1] ^= 1 }, func(p []byte) { p[0] ^= 1 }, func(p []byte) { p[19] |= ikev2.FlagInitiator },
	} {
		bad := append([]byte(nil), request...)
		mutate(bad)
		if reply, err := peer.handle(bad); err == nil || len(reply.Packet) > 0 || reply.Abort != nil {
			t.Fatal("forged request caused a response or action")
		}
	}
	for id := uint32(1); id <= peerResponseCacheSize; id++ {
		if _, err := peer.handle(peerRequest(t, init, id)); err != nil {
			t.Fatal(err)
		}
	}
	if len(peer.cache) != peerResponseCacheSize {
		t.Fatal("unbounded replay cache")
	}
	if _, err := peer.handle(request); err == nil {
		t.Fatal("evicted stale request was applied again")
	}
}

func TestPeerInformationalCarriesLegacyEquipmentAndConfigurationReplies(t *testing.T) {
	peer, init := peerFixture(t)
	request := peerRequest(t, init, 0, ikev2.NotifyWithZeroSPI(deviceIdentityNotify, []byte{0, 1, 1}))
	reply, err := peer.handle(request)
	if err != nil {
		t.Fatal(err)
	}
	inner := decodePeerReply(t, init, 0, reply)
	notify, err := ikev2.ParseNotify(inner[0].Body)
	if err != nil || notify.NotifyType != deviceIdentityNotify || hex.EncodeToString(notify.NotificationData) != "00090121436587092143f5" {
		t.Fatalf("equipment response=%x err=%v", notify.NotificationData, err)
	}
	request = peerRequest(t, init, 1, ikev2.NotifyWithZeroSPI(deviceIdentityNotify, nil))
	reply, err = peer.handle(request)
	if err != nil {
		t.Fatal(err)
	}
	notify, err = ikev2.ParseNotify(decodePeerReply(t, init, 1, reply)[0].Body)
	if err != nil || hex.EncodeToString(notify.NotificationData) != "0009022143658709214300" {
		t.Fatal("legacy derived IMEISV changed")
	}
	var applied []string
	var updates int
	peer.updatePCSCF = func(addresses []string) { applied = addresses; updates++ }
	cp, err := ikev2.ConfigurationPayload(ikev2.Configuration{Type: ikev2.CFGRequest, Attributes: []ikev2.ConfigurationAttribute{
		{Type: ikev2.ConfigPCSCFIPv4Address, Value: []byte{10, 0, 0, 2}}, {Type: ikev2.ConfigPCSCFIPv6Address},
	}})
	if err != nil {
		t.Fatal(err)
	}
	request = peerRequest(t, init, 2, cp)
	reply, err = peer.handle(request)
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := ikev2.ParseConfiguration(decodePeerReply(t, init, 2, reply)[0].Body)
	if err != nil || configuration.Type != ikev2.CFGReply || len(configuration.Attributes) != 2 {
		t.Fatal("restoration reply invalid")
	}
	for _, a := range configuration.Attributes {
		if len(a.Value) != 0 {
			t.Fatal("CFG reply copied requested values")
		}
	}
	if len(applied) > 0 || reply.Abort != nil || reply.AfterSend == nil || peer.needsRebind() {
		t.Fatal("restoration applied before acknowledgement")
	}
	reply.AfterSend()
	duplicate, err := peer.handle(request)
	if err != nil {
		t.Fatal(err)
	}
	duplicate.AfterSend()
	if updates != 1 || len(applied) != 1 || applied[0] != "10.0.0.2" {
		t.Fatal("restoration repeated or lost observed P-CSCF")
	}
	layers := (&upstreamRuntime{peer: peer}).Layers()
	if !layers.Tunnel.Available || layers.IMS.Available || layers.Voice.Available {
		t.Fatal("IMS restoration tore down or misreported the live tunnel")
	}
}

func TestPeerDeleteUsesCurrentCommittedChildAndKeepsReason(t *testing.T) {
	peer, init := peerFixture(t)
	oldDelete, err := ikev2.ESPDeletePayload([]byte{5, 6, 7, 8})
	if err != nil {
		t.Fatal(err)
	}
	peer.setChild([]byte{9, 10, 11, 12}, []byte{13, 14, 15, 16})
	oldReply, err := peer.handle(peerRequest(t, init, 0, oldDelete))
	if err != nil || oldReply.Abort != nil {
		t.Fatal("retired child delete tore down the current child")
	}
	currentDelete, err := ikev2.ESPDeletePayload([]byte{13, 14, 15, 16})
	if err != nil {
		t.Fatal(err)
	}
	reply, err := peer.handle(peerRequest(t, init, 1, currentDelete))
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := ikev2.ParseDelete(decodePeerReply(t, init, 1, reply)[0].Body)
	if err != nil || len(deleted.SPIs) != 1 || !bytes.Equal(deleted.SPIs[0], []byte{9, 10, 11, 12}) {
		t.Fatal("wrong paired SPI acknowledged")
	}
	var stage *StageError
	if !errors.As(reply.Abort, &stage) || stage.Code != "peer_child_deleted" {
		t.Fatal("peer deletion lost its fault layer")
	}
	runtime := &upstreamRuntime{fault: reply.Abort}
	layers := runtime.Layers()
	if layers.Tunnel.Available || layers.Voice.Available || layers.Tunnel.Code != "peer_child_deleted" {
		t.Fatal("peer deletion reported healthy or blamed generic stack")
	}
	reply, err = peer.handle(peerRequest(t, init, 2, ikev2.IKEDeletePayload()))
	if err != nil || len(decodePeerReply(t, init, 2, reply)) != 0 || reply.Abort == nil {
		t.Fatal("IKE deletion did not return terminal empty response")
	}
}

func TestPeerCookieEchoAndExplicitRekeyRejection(t *testing.T) {
	peer, init := peerFixture(t)
	cookie := []byte("12345678")
	payload, err := ikev2.Cookie2Notify(cookie)
	if err != nil {
		t.Fatal(err)
	}
	reply, err := peer.handle(peerRequest(t, init, 0, payload))
	if err != nil {
		t.Fatal(err)
	}
	notify, err := ikev2.ParseNotify(decodePeerReply(t, init, 0, reply)[0].Body)
	if err != nil || notify.NotifyType != ikev2.NotifyCookie2 || !bytes.Equal(notify.NotificationData, cookie) || reply.Abort != nil {
		t.Fatal("COOKIE2 was not echoed")
	}
	payloads, _, _, err := ikev2.BuildCreateChildSAPayloads(ikev2.CreateChildSAConfig{ChildSPI: []byte{9, 10, 11, 12}, RekeySPI: []byte{5, 6, 7, 8}, Nonce: bytes.Repeat([]byte{0x42}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	_, raw, err := ikev2.ProtectMessage(ikev2.Header{InitiatorSPI: init.InitiatorSPI, ResponderSPI: init.ResponderSPI, ExchangeType: ikev2.ExchangeCREATE_CHILD_SA, MessageID: 1}, init.Keys, false, payloads, nil)
	if err != nil {
		t.Fatal(err)
	}
	reply, err = peer.handle(raw)
	if err != nil {
		t.Fatal(err)
	}
	message, inner, err := ikev2.UnprotectMessage(reply.Packet, init.Keys, true)
	if err != nil || message.Header.ExchangeType != ikev2.ExchangeCREATE_CHILD_SA || message.Header.Flags != ikev2.FlagInitiator|ikev2.FlagResponse {
		t.Fatal("rekey rejection has wrong exchange identity")
	}
	notify, err = ikev2.ParseNotify(inner[0].Body)
	if err != nil || notify.NotifyType != ikev2.NotifyNoProposalChosen || reply.Abort != nil {
		t.Fatal("unsupported rekey pretended success or tore down current SA")
	}
}

func TestPeerConfigurationRejectsUnsafeAddressWithoutAction(t *testing.T) {
	for _, value := range [][]byte{{127, 0, 0, 1}, {0, 0, 0, 0}, {1, 2, 3}} {
		peer, init := peerFixture(t)
		cp, err := ikev2.ConfigurationPayload(ikev2.Configuration{Type: ikev2.CFGRequest, Attributes: []ikev2.ConfigurationAttribute{{Type: ikev2.ConfigPCSCFIPv4Address, Value: value}}})
		if err != nil {
			t.Fatal(err)
		}
		if reply, err := peer.handle(peerRequest(t, init, 0, cp)); err == nil || len(reply.Packet) != 0 || reply.Abort != nil || reply.AfterSend != nil {
			t.Fatal("invalid restoration had side effects")
		}
	}
}

func TestPeerRestorationCannotRewriteDesiredOrNewerSessionObservation(t *testing.T) {
	factory := &UpstreamFactory{peerEpoch: 2, config: UpstreamConfig{PCSCF: []string{"configured.example"}}}
	factory.observePeerPCSCF(2, []string{"10.0.0.2"})
	factory.observePeerPCSCF(1, []string{"10.0.0.1"})
	if len(factory.observedPCSCF) != 1 || factory.observedPCSCF[0] != "10.0.0.2" || factory.config.PCSCF[0] != "configured.example" {
		t.Fatal("stale peer changed newer observation or desired config")
	}
}

func TestProviderRequiresIdleForPeerIMSRestoration(t *testing.T) {
	blocked := vowifiipc.LayerStatus{Condition: vowifiipc.LayerBlocked, Code: vowifiipc.PeerPCSCFChanged}
	runtime := &fakeRuntime{layers: &Layers{Tunnel: vowifiipc.LayerStatus{Condition: vowifiipc.LayerReady, Available: true}, IMS: blocked, Voice: blocked, Messaging: blocked}}
	backend, err := NewBackend("line-1", "native", "process-1", &fakeFactory{run: runtime})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Start(t.Context(), vowifiipc.LifecycleRequest{OperationID: "start"}); err != nil {
		t.Fatal(err)
	}
	backend.activeCall = &activeVoiceCall{}
	if _, err := backend.Stop(t.Context(), vowifiipc.LifecycleRequest{OperationID: "rebind", RequireIdle: true}); operationCode(err) != "active_call" || runtime.closes.Load() != 0 {
		t.Fatal("IMS rebind interrupted call")
	}
	backend.activeCall = nil
	if _, err := backend.Stop(t.Context(), vowifiipc.LifecycleRequest{OperationID: "rebind", RequireIdle: true}); err != nil || runtime.closes.Load() != 1 {
		t.Fatalf("idle restoration not closed: %v", err)
	}
}

func TestPeerIKEAndOutboundExchangeShareSocketWithoutBlockingEachOther(t *testing.T) {
	peer, init := peerFixture(t)
	server, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	transport, err := outerudp.New(outerudp.Config{DialContext: func(ctx context.Context, _, _ string, _ time.Duration) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "udp", server.LocalAddr().String())
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close(context.Background())
	if err := transport.Bind("192.0.2.10:4500", time.Second); err != nil {
		t.Fatal(err)
	}
	if err := transport.ServePeerIKE(peer.handle); err != nil {
		t.Fatal(err)
	}
	if err := transport.SendNATTKeepalive(t.Context()); err != nil {
		t.Fatal(err)
	}
	read := func() ([]byte, net.Addr) {
		t.Helper()
		_ = server.SetReadDeadline(time.Now().Add(2 * time.Second))
		b := make([]byte, 65536)
		n, addr, err := server.ReadFrom(b)
		if err != nil {
			t.Fatal(err)
		}
		return b[:n], addr
	}
	_, remote := read()
	_, localRequest, err := ikev2.BuildInformationalRequest(init, init.Keys, 7, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		raw, err := transport.ExchangeIKE(t.Context(), localRequest)
		if err == nil {
			_, _, err = ikev2.ParseInformationalResponse(raw, init, init.Keys, 7)
		}
		done <- err
	}()
	read()
	peerWire := append([]byte{0, 0, 0, 0}, peerRequest(t, init, 0)...)
	if _, err = server.WriteTo(peerWire, remote); err != nil {
		t.Fatal(err)
	}
	response, _ := read()
	decodePeerReply(t, init, 0, outerudp.PeerReply{Packet: response[4:]})
	_, localReply, err := ikev2.BuildInformationalResponse(init, init.Keys, 7, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = server.WriteTo(append([]byte{0, 0, 0, 0}, localReply...), remote); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("outbound exchange deadlocked behind peer handler")
	}
	if _, err = server.WriteTo(append([]byte{0, 0, 0, 0}, peerRequest(t, init, 1, ikev2.IKEDeletePayload())...), remote); err != nil {
		t.Fatal(err)
	}
	response, _ = read()
	decodePeerReply(t, init, 1, outerudp.PeerReply{Packet: response[4:]})
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, err := transport.ReadESPPacket(ctx); err == nil {
		t.Fatal("peer deletion did not terminate old ESP association")
	}
}
