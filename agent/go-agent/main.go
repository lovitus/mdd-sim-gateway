package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ebfe/scard"
)

const (
	vpcdCtrlOff   = 0x01
	vpcdCtrlOn    = 0x02
	vpcdCtrlReset = 0x03
	vpcdCtrlATR   = 0x04
)

// isForbiddenAPDU blocks physical profile delete APDUs
func isForbiddenAPDU(apdu []byte) bool {
	if len(apdu) < 4 {
		return false
	}
	// SGP.22 ES10c.DeleteProfile tag: 0xBF33
	if bytes.Contains(apdu, []byte{0xBF, 0x33}) {
		log.Println("[GUARD] Blocked ES10c.DeleteProfile APDU (tag 0xBF33)")
		return true
	}
	// ISO 7816-4 DELETE FILE (INS=0xE4)
	if apdu[1] == 0xE4 {
		log.Println("[GUARD] Blocked ISO 7816 DELETE FILE APDU (INS=0xE4)")
		return true
	}
	return false
}

// -----------------------------------------------------------------------------
// TOFU (Trust On First Use) Certificate Fingerprint Pinning
// -----------------------------------------------------------------------------

var (
	pinLock      sync.Mutex
	identityLock sync.Mutex
)

func getPinStorePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	dir := filepath.Join(home, ".mdd-agent")
	_ = os.MkdirAll(dir, 0700)
	return filepath.Join(dir, "known_fingerprints.json")
}

func getAgentID() string {
	identityLock.Lock()
	defer identityLock.Unlock()
	path := filepath.Join(filepath.Dir(getPinStorePath()), "identity.json")
	var stored struct {
		AgentID string `json:"agent_id"`
	}
	if data, err := os.ReadFile(path); err == nil && json.Unmarshal(data, &stored) == nil && stored.AgentID != "" {
		return stored.AgentID
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return fmt.Sprintf("agent-%d", time.Now().UnixNano())
	}
	stored.AgentID = hex.EncodeToString(random)
	if data, err := json.MarshalIndent(stored, "", "  "); err == nil {
		_ = os.WriteFile(path, data, 0600)
	}
	return stored.AgentID
}

func stableReaderID(name string) string {
	normalized := strings.ToLower(strings.Join(strings.Fields(name), " "))
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:12])
}

func agentWSPath(path string, readerName string) string {
	u, err := url.Parse(path)
	if err != nil {
		return path
	}
	query := u.Query()
	if query.Get("slot") == "" {
		query.Set("slot", "auto")
	}
	query.Set("agent_id", getAgentID())
	query.Set("reader_id", stableReaderID(readerName))
	query.Set("reader_name", readerName)
	u.RawQuery = query.Encode()
	return u.String()
}

func safeWSLogPath(path string) string {
	u, err := url.Parse(path)
	if err != nil {
		return strings.SplitN(path, "?", 2)[0]
	}
	u.RawQuery = ""
	u.ForceQuery = false
	return u.String()
}

func loadPinStore() map[string]string {
	pinLock.Lock()
	defer pinLock.Unlock()

	store := make(map[string]string)
	data, err := os.ReadFile(getPinStorePath())
	if err == nil {
		_ = json.Unmarshal(data, &store)
	}
	return store
}

func savePinStore(store map[string]string) {
	pinLock.Lock()
	defer pinLock.Unlock()

	data, err := json.MarshalIndent(store, "", "  ")
	if err == nil {
		_ = os.WriteFile(getPinStorePath(), data, 0600)
	}
}

func formatFingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	hexStr := strings.ToUpper(hex.EncodeToString(sum[:]))
	var parts []string
	for i := 0; i < len(hexStr); i += 2 {
		parts = append(parts, hexStr[i:i+2])
	}
	return strings.Join(parts, ":")
}

func verifyOrPinFingerprint(targetHost string, rawCert []byte, explicitPin string, resetPin bool) error {
	currentFp := formatFingerprint(rawCert)

	if explicitPin != "" {
		cleanExpected := strings.ToUpper(strings.ReplaceAll(explicitPin, ":", ""))
		cleanActual := strings.ToUpper(strings.ReplaceAll(currentFp, ":", ""))
		if cleanExpected != cleanActual {
			return fmt.Errorf("[SECURITY ALERT] ⚠️ Certificate fingerprint mismatch!\n  Expected: %s\n  Actual:   %s", explicitPin, currentFp)
		}
		log.Printf("[SECURITY] ✅ Verified against explicit certificate pin (%s)\n", currentFp)
		return nil
	}

	store := loadPinStore()
	pinnedFp, exists := store[targetHost]

	if resetPin {
		store[targetHost] = currentFp
		savePinStore(store)
		log.Printf("[SECURITY] 🔄 Reset and updated pinned fingerprint for %s -> %s\n", targetHost, currentFp)
		return nil
	}

	if !exists {
		// Trust On First Use (TOFU)
		store[targetHost] = currentFp
		savePinStore(store)
		log.Printf("[SECURITY] 🔒 First time connecting to %s. Pinned server certificate fingerprint (SHA-256):\n           %s\n", targetHost, currentFp)
		return nil
	}

	if pinnedFp != currentFp {
		return fmt.Errorf("[SECURITY ALERT] ⚠️ Server TLS certificate fingerprint MISMATCH for %s!\n"+
			"  Previous Pinned: %s\n"+
			"  Current Server:  %s\n"+
			"  Possible Man-In-The-Middle (MITM) attack or certificate renewal!\n"+
			"  Connection ABORTED. To trust this new certificate, rerun with '-reset-pin'", targetHost, pinnedFp, currentFp)
	}

	log.Printf("[SECURITY] ✅ TLS certificate fingerprint verified: %s\n", currentFp)
	return nil
}

// -----------------------------------------------------------------------------
// WebSocket Client Transport
// -----------------------------------------------------------------------------

type wsConn struct {
	conn         net.Conn
	br           *bufio.Reader
	readPending  []byte
	writePending []byte
	writeMu      sync.Mutex
	frameMu      sync.Mutex
}

func (w *wsConn) Read(p []byte) (int, error) {
	if len(w.readPending) > 0 {
		n := copy(p, w.readPending)
		w.readPending = w.readPending[n:]
		return n, nil
	}
	for {
		header, err := w.br.ReadByte()
		if err != nil {
			return 0, err
		}
		fin := (header & 0x80) != 0
		opcode := header & 0x0F

		b2, err := w.br.ReadByte()
		if err != nil {
			return 0, err
		}
		isMasked := (b2 & 0x80) != 0
		length := uint64(b2 & 0x7F)

		if length == 126 {
			var extended uint16
			if err := binary.Read(w.br, binary.BigEndian, &extended); err != nil {
				return 0, err
			}
			length = uint64(extended)
		} else if length == 127 {
			var extended uint64
			if err := binary.Read(w.br, binary.BigEndian, &extended); err != nil {
				return 0, err
			}
			length = extended
		}

		var maskKey [4]byte
		if isMasked {
			if _, err := io.ReadFull(w.br, maskKey[:]); err != nil {
				return 0, err
			}
		}

		payload := make([]byte, length)
		if _, err := io.ReadFull(w.br, payload); err != nil {
			return 0, err
		}

		if isMasked {
			for i := uint64(0); i < length; i++ {
				payload[i] ^= maskKey[i%4]
			}
		}

		if opcode == 0x08 { // Close
			return 0, io.EOF
		}
		if opcode == 0x09 { // Ping -> Send Pong
			w.writeFrame(0x0A, payload)
			continue
		}
		if opcode == 0x0A { // Pong
			continue
		}

		if opcode == 0x02 || opcode == 0x01 || (fin && opcode == 0x00) { // Binary or Text
			if len(payload) > 0xFFFF {
				return 0, errors.New("VPCD WebSocket frame exceeds 65535 bytes")
			}
			w.readPending = make([]byte, 2+len(payload))
			binary.BigEndian.PutUint16(w.readPending, uint16(len(payload)))
			copy(w.readPending[2:], payload)
			n := copy(p, w.readPending)
			w.readPending = w.readPending[n:]
			return n, nil
		}
	}
}

func (w *wsConn) writeFrame(opcode byte, data []byte) error {
	w.frameMu.Lock()
	defer w.frameMu.Unlock()
	var header bytes.Buffer
	header.WriteByte(0x80 | opcode) // FIN + opcode

	length := len(data)
	if length < 126 {
		header.WriteByte(0x80 | byte(length)) // Masked
	} else if length <= 65535 {
		header.WriteByte(0x80 | 126)
		_ = binary.Write(&header, binary.BigEndian, uint16(length))
	} else {
		header.WriteByte(0x80 | 127)
		_ = binary.Write(&header, binary.BigEndian, uint64(length))
	}

	var mask [4]byte
	_, _ = rand.Read(mask[:])
	header.Write(mask[:])

	maskedData := make([]byte, length)
	for i := 0; i < length; i++ {
		maskedData[i] = data[i] ^ mask[i%4]
	}

	if _, err := w.conn.Write(header.Bytes()); err != nil {
		return err
	}
	_, err := w.conn.Write(maskedData)
	return err
}

func (w *wsConn) Write(p []byte) (int, error) {
	w.writeMu.Lock()
	defer w.writeMu.Unlock()
	w.writePending = append(w.writePending, p...)
	for len(w.writePending) >= 2 {
		length := int(binary.BigEndian.Uint16(w.writePending[:2]))
		if len(w.writePending) < 2+length {
			break
		}
		payload := append([]byte(nil), w.writePending[2:2+length]...)
		w.writePending = w.writePending[2+length:]
		if length > 0 {
			if err := w.writeFrame(0x02, payload); err != nil {
				return 0, err
			}
		}
	}
	return len(p), nil
}

func (w *wsConn) Close() error {
	_ = w.writeFrame(0x08, []byte{}) // Close frame
	return w.conn.Close()
}

func dialWSS(targetHost string, port int, path string, token string, explicitPin string, resetPin bool, readerName string) (io.ReadWriteCloser, error) {
	addr := fmt.Sprintf("%s:%d", targetHost, port)

	var tlsErr error
	tlsConfig := &tls.Config{
		InsecureSkipVerify: true, // We do custom SHA-256 TOFU certificate pinning
		VerifyPeerCertificate: func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return errors.New("no certificate presented by server")
			}
			tlsErr = verifyOrPinFingerprint(targetHost, rawCerts[0], explicitPin, resetPin)
			return tlsErr
		},
	}

	rawConn, err := tls.DialWithDialer(&net.Dialer{Timeout: 10 * time.Second}, "tcp", addr, tlsConfig)
	if err != nil {
		if tlsErr != nil {
			return nil, tlsErr
		}
		return nil, fmt.Errorf("TLS dial failed to %s: %w", addr, err)
	}

	// Generate Sec-WebSocket-Key
	keyBytes := make([]byte, 16)
	_, _ = rand.Read(keyBytes)
	secKey := base64.StdEncoding.EncodeToString(keyBytes)

	// Build request URI
	wsPath := agentWSPath(path, readerName)
	if !strings.HasPrefix(wsPath, "/") {
		wsPath = "/" + wsPath
	}
	if token != "" {
		if strings.Contains(wsPath, "?") {
			wsPath += "&token=" + url.QueryEscape(token)
		} else {
			wsPath += "?token=" + url.QueryEscape(token)
		}
	}

	req := fmt.Sprintf("GET %s HTTP/1.1\r\n"+
		"Host: %s:%d\r\n"+
		"Upgrade: websocket\r\n"+
		"Connection: Upgrade\r\n"+
		"Sec-WebSocket-Key: %s\r\n"+
		"Sec-WebSocket-Version: 13\r\n", wsPath, targetHost, port, secKey)
	if token != "" {
		req += fmt.Sprintf("X-Agent-Token: %s\r\n", token)
	}
	req += "\r\n"

	if _, err := rawConn.Write([]byte(req)); err != nil {
		rawConn.Close()
		return nil, fmt.Errorf("failed to send WebSocket upgrade request: %w", err)
	}

	br := bufio.NewReader(rawConn)
	resp, err := http.ReadResponse(br, &http.Request{Method: "GET"})
	if err != nil {
		rawConn.Close()
		return nil, fmt.Errorf("failed to read WebSocket upgrade response: %w", err)
	}

	if resp.StatusCode != http.StatusSwitchingProtocols {
		rawConn.Close()
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return nil, fmt.Errorf("gateway rejected connection: %s (Check your -token)", resp.Status)
		}
		return nil, fmt.Errorf("WebSocket upgrade rejected by gateway: %s", resp.Status)
	}

	log.Printf("[card-agent] ✅ WSS handshake established on https://%s:%d%s\n",
		targetHost, port, safeWSLogPath(wsPath))
	return &wsConn{conn: rawConn, br: br}, nil
}

// -----------------------------------------------------------------------------
// Main Session Loop
// -----------------------------------------------------------------------------

func main() {
	server := flag.String("server", "", "Gateway address in host:port format (e.g. gateway.example.com:8443)")
	gateway := flag.String("gateway", "127.0.0.1", "Gateway hostname or IP")
	port := flag.Int("port", 8443, "Gateway port (8443 for WSS, 35963 for raw TCP)")
	token := flag.String("token", "", "Gateway agent security token (shared across devices)")
	useWSS := flag.Bool("wss", true, "Use encrypted WSS tunnel with TOFU certificate pinning")
	wsPath := flag.String("path", "/mdd/api/vpcd/ws", "WebSocket bridge path on gateway")
	explicitPin := flag.String("pin", "", "Expected SHA-256 certificate fingerprint (e.g. SHA256:XX:XX:...)")
	resetPin := flag.Bool("reset-pin", false, "Reset and trust the current server certificate fingerprint")
	readerSub := flag.String("reader", "", "Substring match for PC/SC reader name")
	retrySec := flag.Int("retry", 3, "Retry interval in seconds")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "MDD VoWiFi Gateway - Smartcard Forwarding Agent (Go)\n\n")
		fmt.Fprintf(os.Stderr, "Usage:\n")
		fmt.Fprintf(os.Stderr, "  %s [options]\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Options:\n")
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nExamples:\n")
		fmt.Fprintf(os.Stderr, "  # 1. Connect via WSS with Token (Auto TOFU Pinning):\n")
		fmt.Fprintf(os.Stderr, "  %s -gateway gateway.example.com -port 8443 -token \"<AGENT_TOKEN>\"\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  # 2. Connect specifying server host:port shorthand:\n")
		fmt.Fprintf(os.Stderr, "  %s -server gateway.example.com:8443 -token \"<AGENT_TOKEN>\"\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  # 3. Connect filtering a specific smartcard reader:\n")
		fmt.Fprintf(os.Stderr, "  %s -gateway gateway.example.com -token \"<AGENT_TOKEN>\" -reader \"OMNIKEY\"\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  # 4. Reset and trust a newly rotated server certificate:\n")
		fmt.Fprintf(os.Stderr, "  %s -gateway gateway.example.com -token \"<AGENT_TOKEN>\" -reset-pin\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  # 5. Connect with explicit certificate fingerprint pinning:\n")
		fmt.Fprintf(os.Stderr, "  %s -gateway gateway.example.com -token \"<AGENT_TOKEN>\" -pin \"75:9E:08:73:9F:...\"\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  # 6. Legacy LAN raw TCP connection (Unencrypted):\n")
		fmt.Fprintf(os.Stderr, "  %s -gateway gateway.example.com -port 35963 -wss=false\n\n", os.Args[0])
	}
	flag.Parse()
	if !acquireUnifiedAgentLease() {
		log.Fatal("the unified MDD Agent service or another Card Agent owns local devices")
	}

	// Parse -server shorthand if supplied
	if *server != "" {
		if h, p, err := net.SplitHostPort(*server); err == nil {
			*gateway = h
			var parsedPort int
			if _, err := fmt.Sscanf(p, "%d", &parsedPort); err == nil && parsedPort > 0 {
				*port = parsedPort
			}
		} else {
			*gateway = *server
		}
	}

	// If user explicitly specified port 35963 and did not force -wss, default to raw TCP
	if *port == 35963 && !isFlagPassed("wss") {
		*useWSS = false
	}

	// Environment variable fallback for token
	if *token == "" {
		if envToken := os.Getenv("MDD_AGENT_TOKEN"); envToken != "" {
			*token = envToken
		} else if envToken := os.Getenv("MDD_TOKEN"); envToken != "" {
			*token = envToken
		}
	}

	protocol := "WSS (Encrypted + TOFU Pinning)"
	if !*useWSS {
		protocol = "Raw TCP (Unencrypted)"
	}
	log.Printf("[card-agent] Starting Smartcard Forwarder [%s] -> %s:%d\n", protocol, *gateway, *port)

	if *useWSS && *readerSub == "" {
		runReaderSupervisor(*gateway, *port, *wsPath, *token, *explicitPin,
			*resetPin, time.Duration(*retrySec)*time.Second)
		return
	}
	runReaderWorker(*gateway, *port, *wsPath, *token, *useWSS, *explicitPin,
		*resetPin, *readerSub, time.Duration(*retrySec)*time.Second)
}

func isFlagPassed(name string) bool {
	found := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

func listReaderNames() ([]string, error) {
	ctx, err := scard.EstablishContext()
	if err != nil {
		return nil, fmt.Errorf("failed to establish PC/SC context: %w", err)
	}
	defer ctx.Release()
	names, err := ctx.ListReaders()
	if err != nil {
		return nil, err
	}
	return names, nil
}

func runReaderSupervisor(host string, port int, wsPath string, token string,
	explicitPin string, resetPin bool, retryDelay time.Duration) {
	workers := make(map[string]struct{})
	lastDiscoveryError := ""
	emptyAnnounced := false
	for {
		names, err := listReaderNames()
		if err != nil {
			message := err.Error()
			if message != lastDiscoveryError {
				log.Printf("[card-agent] PC/SC discovery failed: %v", err)
				lastDiscoveryError = message
			}
		} else {
			if lastDiscoveryError != "" {
				log.Printf("[card-agent] PC/SC discovery recovered")
				lastDiscoveryError = ""
			}
			if len(names) == 0 && !emptyAnnounced {
				log.Printf("[card-agent] No PC/SC readers currently attached; watching for hotplug")
				emptyAnnounced = true
			}
			if len(names) > 0 {
				emptyAnnounced = false
			}
			for _, name := range newReaderNames(names, workers) {
				workers[name] = struct{}{}
				log.Printf("[card-agent] Starting hotplug worker for '%s'", name)
				go runReaderWorker(host, port, wsPath, token, true, explicitPin,
					resetPin, name, retryDelay)
			}
		}
		time.Sleep(retryDelay)
	}
}

func newReaderNames(discovered []string, workers map[string]struct{}) []string {
	result := make([]string, 0, len(discovered))
	seen := make(map[string]struct{}, len(discovered))
	for _, name := range discovered {
		if _, exists := workers[name]; exists {
			continue
		}
		if _, duplicate := seen[name]; duplicate {
			continue
		}
		seen[name] = struct{}{}
		result = append(result, name)
	}
	return result
}

func runReaderWorker(host string, port int, wsPath string, token string, useWSS bool,
	explicitPin string, resetPin bool, readerFilter string, retryDelay time.Duration) {
	for {
		err := runSession(host, port, wsPath, token, useWSS, explicitPin, resetPin, readerFilter)
		if err != nil {
			log.Printf("[card-agent] Reader '%s' session ended: %v. Retrying in %s...",
				readerFilter, err, retryDelay)
		}
		resetPin = false
		time.Sleep(retryDelay)
	}
}

func selectReaderName(names []string, filter string) (string, bool) {
	if len(names) == 0 {
		return "", false
	}
	if filter == "" {
		return names[0], true
	}
	for _, name := range names {
		if strings.EqualFold(name, filter) {
			return name, true
		}
	}
	pattern := strings.ToLower(filter)
	for _, name := range names {
		if strings.Contains(strings.ToLower(name), pattern) {
			return name, true
		}
	}
	return "", false
}

func runSession(host string, port int, wsPath string, token string, useWSS bool, explicitPin string, resetPin bool, readerFilter string) error {
	ctx, err := scard.EstablishContext()
	if err != nil {
		return fmt.Errorf("failed to establish PC/SC context: %w", err)
	}
	defer ctx.Release()

	readers, err := ctx.ListReaders()
	if err != nil || len(readers) == 0 {
		return fmt.Errorf("no PC/SC smartcard readers found (err: %v)", err)
	}

	selected, found := selectReaderName(readers, readerFilter)
	if !found {
		return fmt.Errorf("no reader matching '%s' found in %v", readerFilter, readers)
	}

	card, err := ctx.Connect(selected, scard.ShareShared, scard.ProtocolT0)
	if err != nil {
		card, err = ctx.Connect(selected, scard.ShareShared, scard.ProtocolT1)
	}
	if err != nil {
		card, err = ctx.Connect(selected, scard.ShareShared, scard.ProtocolAny)
	}
	if err != nil {
		return fmt.Errorf("failed to connect to card on reader '%s': %w", selected, err)
	}
	defer card.Disconnect(scard.LeaveCard)

	status, err := card.Status()
	if err != nil {
		return fmt.Errorf("failed to get card status: %w", err)
	}
	atr := status.Atr
	activeProto := status.ActiveProtocol
	log.Printf("[card-agent] Connected to smartcard on '%s' (ATR: %X)\n", selected, atr)

	var conn io.ReadWriteCloser
	if useWSS {
		conn, err = dialWSS(host, port, wsPath, token, explicitPin, resetPin, selected)
		if err != nil {
			return err
		}
	} else {
		addr := fmt.Sprintf("%s:%d", host, port)
		rawTCP, err := net.DialTimeout("tcp", addr, 10*time.Second)
		if err != nil {
			return fmt.Errorf("failed to dial gateway raw TCP %s: %w", addr, err)
		}
		conn = rawTCP
		log.Printf("[card-agent] Connected to raw TCP VPCD %s\n", addr)
	}
	defer conn.Close()

	log.Printf("[card-agent] 🔗 VPCD secure bridge active. Ready to forward APDU frames.\n")

	for {
		header := make([]byte, 2)
		if _, err := io.ReadFull(conn, header); err != nil {
			return fmt.Errorf("read header error: %w", err)
		}

		length := binary.BigEndian.Uint16(header)
		if length == 0 {
			continue
		}

		payload := make([]byte, length)
		if _, err := io.ReadFull(conn, payload); err != nil {
			return fmt.Errorf("read payload error: %w", err)
		}

		if length == 1 {
			ctrl := payload[0]
			if ctrl == vpcdCtrlATR {
				// Send ATR response
				respHeader := make([]byte, 2)
				binary.BigEndian.PutUint16(respHeader, uint16(len(atr)))
				if _, err := conn.Write(respHeader); err != nil {
					return err
				}
				if _, err := conn.Write(atr); err != nil {
					return err
				}
				log.Printf(">> [VPCD] Sent ATR to gateway (%d bytes)\n", len(atr))
			} else if ctrl == vpcdCtrlReset || ctrl == vpcdCtrlOn || ctrl == vpcdCtrlOff {
				log.Printf(">> [VPCD] Received card reset request (ctrl=%d)\n", ctrl)
				_ = card.Reconnect(scard.ShareShared, activeProto, scard.LeaveCard)
			}
			continue
		}

		// APDU command forwarding
		var resp []byte
		if isForbiddenAPDU(payload) {
			resp = []byte{0x69, 0x85} // Blocked by guard
		} else {
			res, err := card.Transmit(payload)
			if err != nil {
				// Reconnect with active protocol and retry once
				_ = card.Reconnect(scard.ShareShared, activeProto, scard.LeaveCard)
				res, err = card.Transmit(payload)
			}
			if err != nil {
				log.Printf("[card-agent] Transmit error on APDU %x: %v\n", payload, err)
				resp = []byte{0x6F, 0x00}
			} else {
				resp = res
			}
		}

		respHeader := make([]byte, 2)
		binary.BigEndian.PutUint16(respHeader, uint16(len(resp)))
		if _, err := conn.Write(respHeader); err != nil {
			return err
		}
		if _, err := conn.Write(resp); err != nil {
			return err
		}
	}
}
