package agentat

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

type transactionPort struct {
	command string
	timeout time.Duration
	ctx     context.Context
}

func (*transactionPort) Read([]byte) (int, error) { return 0, io.EOF }
func (*transactionPort) Write([]byte) (int, error) {
	return 0, errors.New("split write must not be used")
}
func (*transactionPort) Close() error            { return nil }
func (*transactionPort) Drain() error            { return errors.New("split drain must not be used") }
func (*transactionPort) ResetInputBuffer() error { return errors.New("split reset must not be used") }
func (port *transactionPort) Exchange(ctx context.Context, command string, timeout time.Duration) ([]byte, error) {
	port.ctx, port.command, port.timeout = ctx, command, timeout
	return []byte("\r\nOK\r\n"), nil
}

func TestOwnerUsesBoundedTransactionalPortWithoutSplitIO(t *testing.T) {
	port := &transactionPort{}
	owner := &Owner{port: port}
	ctx := context.WithValue(context.Background(), struct{}{}, "request")
	response, err := owner.Exchange(ctx, "AT+CSQ", 1700*time.Millisecond)
	if err != nil || string(response) != "\r\nOK\r\n" || port.ctx != ctx ||
		port.command != "AT+CSQ" || port.timeout != 1700*time.Millisecond {
		t.Fatalf("response=%q command=%q timeout=%s ctx_match=%v err=%v",
			response, port.command, port.timeout, port.ctx == ctx, err)
	}
}
