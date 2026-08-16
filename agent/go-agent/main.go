package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
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

func main() {
	gateway := flag.String("gateway", "127.0.0.1", "Gateway hostname or IP")
	port := flag.Int("port", 35963, "Gateway VPCD port")
	readerSub := flag.String("reader", "", "Substring match for reader name")
	retrySec := flag.Int("retry", 3, "Retry interval in seconds")
	flag.Parse()

	log.Printf("[card-agent] Starting Go Smartcard Forwarder -> %s:%d\n", *gateway, *port)

	for {
		err := runSession(*gateway, *port, *readerSub)
		if err != nil {
			log.Printf("[card-agent] Session ended: %v. Retrying in %d seconds...\n", err, *retrySec)
		}
		time.Sleep(time.Duration(*retrySec) * time.Second)
	}
}

func runSession(host string, port int, readerFilter string) error {
	ctx, err := scard.EstablishContext()
	if err != nil {
		return fmt.Errorf("failed to establish PC/SC context: %w", err)
	}
	defer ctx.Release()

	readers, err := ctx.ListReaders()
	if err != nil || len(readers) == 0 {
		return fmt.Errorf("no PC/SC smartcard readers found (err: %v)", err)
	}

	selected := readers[0]
	if readerFilter != "" {
		found := false
		for _, r := range readers {
			if strings.Contains(strings.ToLower(r), strings.ToLower(readerFilter)) {
				selected = r
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("no reader matching '%s' found in %v", readerFilter, readers)
		}
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
	log.Printf("[card-agent] Connected to card on '%s' (ATR: %X)\n", selected, atr)

	addr := fmt.Sprintf("%s:%d", host, port)
	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		return fmt.Errorf("failed to dial gateway %s: %w", addr, err)
	}
	defer conn.Close()

	log.Printf("[card-agent] VPCD bridge connected to %s. Forwarding APDU commands...\n", addr)

	for {
		var length uint16
		if err := binary.Read(conn, binary.BigEndian, &length); err != nil {
			return fmt.Errorf("read header error: %w", err)
		}
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
				// Send ATR
				respHeader := make([]byte, 2)
				binary.BigEndian.PutUint16(respHeader, uint16(len(atr)))
				if _, err := conn.Write(respHeader); err != nil {
					return err
				}
				if _, err := conn.Write(atr); err != nil {
					return err
				}
			} else if ctrl == vpcdCtrlReset || ctrl == vpcdCtrlOn || ctrl == vpcdCtrlOff {
				if err := card.Reconnect(scard.ShareShared, scard.ProtocolT0, scard.ResetCard); err != nil {
					card.Reconnect(scard.ShareShared, scard.ProtocolT1, scard.ResetCard)
				}
			}

			continue
		}

		// APDU command
		var resp []byte
		if isForbiddenAPDU(payload) {
			resp = []byte{0x69, 0x85} // Blocked
		} else {
			res, err := card.Transmit(payload)
			if err != nil {
				log.Printf("[card-agent] Transmit error: %v\n", err)
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
