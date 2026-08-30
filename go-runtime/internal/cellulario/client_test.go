package cellulario

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

type protocolFrame struct {
	typeID    byte
	requestID uint32
	payload   []byte
}

func readProtocolFrame(reader io.Reader) (protocolFrame, error) {
	header := make([]byte, 12)
	if _, err := io.ReadFull(reader, header); err != nil {
		return protocolFrame{}, err
	}
	length := binary.BigEndian.Uint32(header[8:])
	if header[0] != protocolVersion || length > maximumFrame {
		return protocolFrame{}, errors.New("invalid test protocol frame")
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return protocolFrame{}, err
	}
	return protocolFrame{typeID: header[1], requestID: binary.BigEndian.Uint32(header[4:]), payload: payload}, nil
}

func writeProtocolFrame(writer io.Writer, typeID byte, requestID uint32, payload []byte) error {
	frame := make([]byte, 12+len(payload))
	frame[0], frame[1] = protocolVersion, typeID
	binary.BigEndian.PutUint32(frame[4:], requestID)
	binary.BigEndian.PutUint32(frame[8:], uint32(len(payload)))
	copy(frame[12:], payload)
	return writeFull(writer, frame)
}

func writeProtocolResponse(writer io.Writer, requestID uint32, status int32, payload []byte) error {
	value := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(value, uint32(status))
	copy(value[4:], payload)
	return writeProtocolFrame(writer, messageResponse, requestID, value)
}

func expectProtocol(reader io.Reader, expected byte) (protocolFrame, error) {
	frame, err := readProtocolFrame(reader)
	if err != nil {
		return frame, err
	}
	if frame.typeID != expected {
		return frame, fmt.Errorf("message type=%d, want %d", frame.typeID, expected)
	}
	return frame, nil
}

func TestClientPreservesEarlyTCPAndUDPFrames(t *testing.T) {
	parent, companion := net.Pipe()
	defer companion.Close()
	client, err := newClient(parent, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_ = companion.SetDeadline(time.Now().Add(10 * time.Second))
	serverDone := make(chan error, 1)
	go func() {
		hello, err := expectProtocol(companion, messageHello)
		if err != nil {
			serverDone <- err
			return
		}
		if err := writeProtocolResponse(companion, hello.requestID, 0, []byte("version=1;at_transactions=2;sms_submit=1;serial=unit")); err != nil {
			serverDone <- err
			return
		}
		at, err := expectProtocol(companion, messageATCommandV2)
		if err != nil || len(at.payload) < 6 || binary.BigEndian.Uint32(at.payload[:4]) != 1000 || string(at.payload[4:]) != "AT+CSQ" {
			serverDone <- fmt.Errorf("AT frame=%+v err=%v", at, err)
			return
		}
		if err := writeProtocolResponse(companion, at.requestID, 0, []byte("\r\n+CSQ: 20,99\r\nOK\r\n")); err != nil {
			serverDone <- err
			return
		}
		sms, err := expectProtocol(companion, messageSMSSubmit)
		if err != nil || len(sms.payload) < 4 {
			serverDone <- fmt.Errorf("SMS frame=%+v err=%v", sms, err)
			return
		}
		commandLength := int(binary.BigEndian.Uint16(sms.payload))
		if commandLength < 9 || 2+commandLength >= len(sms.payload) ||
			!strings.HasPrefix(string(sms.payload[2:2+commandLength]), "AT+CMGS=") {
			serverDone <- errors.New("typed SMS frame is malformed")
			return
		}
		if err := writeProtocolResponse(companion, sms.requestID, 0, []byte("\r\n+CMGS: 9\r\nOK\r\n")); err != nil {
			serverDone <- err
			return
		}
		tcpOpen, err := expectProtocol(companion, messageTCPOpen)
		if err != nil {
			serverDone <- err
			return
		}
		handle := make([]byte, 4)
		binary.BigEndian.PutUint32(handle, 101)
		if err := writeProtocolResponse(companion, tcpOpen.requestID, 0, handle); err != nil {
			serverDone <- err
			return
		}
		if err := writeProtocolFrame(companion, messageTCPData, 0, append(handle, []byte("early")...)); err != nil {
			serverDone <- err
			return
		}
		tcpWrite, err := expectProtocol(companion, messageTCPWrite)
		if err != nil || len(tcpWrite.payload) != 8 || string(tcpWrite.payload[4:]) != "send" {
			serverDone <- fmt.Errorf("TCP write=%+v err=%v", tcpWrite, err)
			return
		}
		if err := writeProtocolResponse(companion, tcpWrite.requestID, 0, nil); err != nil {
			serverDone <- err
			return
		}
		tcpClose, err := expectProtocol(companion, messageTCPClose)
		if err != nil {
			serverDone <- err
			return
		}
		if err := writeProtocolResponse(companion, tcpClose.requestID, 0, nil); err != nil {
			serverDone <- err
			return
		}
		udpOpen, err := expectProtocol(companion, messageUDPOpen)
		if err != nil {
			serverDone <- err
			return
		}
		binary.BigEndian.PutUint32(handle, 102)
		if err := writeProtocolResponse(companion, udpOpen.requestID, 0, handle); err != nil {
			serverDone <- err
			return
		}
		for _, packet := range []string{"first", "second"} {
			value := make([]byte, 10+len(packet))
			copy(value, handle)
			binary.BigEndian.PutUint16(value[4:], 53)
			copy(value[6:], []byte{1, 1, 1, 1})
			copy(value[10:], packet)
			if err := writeProtocolFrame(companion, messageUDPData, 0, value); err != nil {
				serverDone <- err
				return
			}
		}
		udpWrite, err := expectProtocol(companion, messageUDPSend)
		if err != nil || len(udpWrite.payload) < 4 || string(udpWrite.payload[len(udpWrite.payload)-5:]) != "query" {
			serverDone <- fmt.Errorf("UDP write=%+v err=%v", udpWrite, err)
			return
		}
		if err := writeProtocolResponse(companion, udpWrite.requestID, 0, nil); err != nil {
			serverDone <- err
			return
		}
		udpClose, err := expectProtocol(companion, messageUDPClose)
		if err == nil {
			err = writeProtocolResponse(companion, udpClose.requestID, 0, nil)
		}
		serverDone <- err
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := client.initialize(ctx); err != nil {
		t.Fatal(err)
	}
	if value, err := client.AT(ctx, "AT+CSQ", 1200*time.Millisecond); err != nil || !strings.Contains(string(value), "+CSQ") {
		t.Fatalf("AT response=%q err=%v", value, err)
	}
	if value, uncertain, err := client.SubmitSMSPDU(ctx, 10, "001122"); err != nil || uncertain || !strings.Contains(string(value), "+CMGS") {
		t.Fatalf("SMS response=%q uncertain=%v err=%v", value, uncertain, err)
	}
	tcp, err := client.OpenTCP(ctx, "example.com:443")
	if err != nil {
		t.Fatal(err)
	}
	tcpBuffer := make([]byte, 5)
	if count, err := io.ReadFull(tcp, tcpBuffer); err != nil || count != 5 || string(tcpBuffer) != "early" {
		t.Fatalf("TCP early data=%q count=%d err=%v", tcpBuffer, count, err)
	}
	if count, err := tcp.Write([]byte("send")); err != nil || count != 4 {
		t.Fatalf("TCP write count=%d err=%v", count, err)
	}
	if err := tcp.Close(); err != nil {
		t.Fatal(err)
	}
	udp, err := client.OpenUDP(ctx, "1.1.1.1:53")
	if err != nil {
		t.Fatal(err)
	}
	short := make([]byte, 3)
	if count, err := udp.Read(short); err != nil || count != 3 || string(short) != "fir" {
		t.Fatalf("first UDP datagram=%q count=%d err=%v", short, count, err)
	}
	full := make([]byte, 16)
	if count, err := udp.Read(full); err != nil || string(full[:count]) != "second" {
		t.Fatalf("second UDP datagram=%q count=%d err=%v", full[:count], count, err)
	}
	if count, err := udp.Write([]byte("query")); err != nil || count != 5 {
		t.Fatalf("UDP write count=%d err=%v", count, err)
	}
	if err := udp.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestTypedSMSUnknownOutcomeIsNotRetryable(t *testing.T) {
	parent, companion := net.Pipe()
	defer companion.Close()
	client, err := newClient(parent, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	serverDone := make(chan error, 1)
	go func() {
		request, err := expectProtocol(companion, messageSMSSubmit)
		if err == nil {
			err = writeProtocolResponse(companion, request.requestID, -5, []byte("sms_submit_unknown_after_pdu"))
		}
		serverDone <- err
	}()
	_, possiblySent, err := client.SubmitSMSPDU(context.Background(), 10, "001122")
	if err == nil || !possiblySent || !strings.Contains(err.Error(), "unknown_after_pdu") {
		t.Fatalf("possibly_sent=%v err=%v", possiblySent, err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestClientRejectsCompanionWithoutAtomicSMSContract(t *testing.T) {
	parent, companion := net.Pipe()
	defer companion.Close()
	client, err := newClient(parent, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	serverDone := make(chan error, 1)
	go func() {
		hello, err := expectProtocol(companion, messageHello)
		if err == nil {
			err = writeProtocolResponse(companion, hello.requestID, 0, []byte("version=1;at_transactions=2"))
		}
		serverDone <- err
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := client.initialize(ctx); err == nil || !strings.Contains(err.Error(), "incompatible") {
		t.Fatalf("initialize error=%v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}
