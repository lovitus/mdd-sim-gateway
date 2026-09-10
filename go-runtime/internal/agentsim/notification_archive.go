package agentsim

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/damonto/euicc-go/bertlv"
	"github.com/damonto/euicc-go/lpa"
	sgp22 "github.com/damonto/euicc-go/v2"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
)

func archivedEUICCNotification(ctx context.Context, card Card, aid []byte, request agentlink.EUICCNotificationRequest) (payload []byte, ack bool, err error) {
	defer func() {
		if value := recover(); value != nil {
			payload = nil
			err = fmt.Errorf("notification decode failed")
		}
	}()
	client, err := lpa.New(&lpa.Options{Channel: &euiccCardChannel{ctx: ctx, card: card}, AID: append([]byte(nil), aid...), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		return nil, false, err
	}
	defer func() { err = errors.Join(err, client.Close()) }()
	return archivedEUICCNotificationWithClient(ctx, client, request)
}

func archivedEUICCNotificationWithClient(ctx context.Context, client *lpa.Client, request agentlink.EUICCNotificationRequest) (payload []byte, ack bool, err error) {
	if request.Action == agentlink.EUICCNotificationArchive {
		selected, err := retrieveExpectedNotification(client, *request.Expected)
		if err != nil {
			return nil, false, err
		}
		payload, err = selected.PendingNotification.MarshalBinary()
		if len(payload) > 65536 {
			return nil, false, errors.New("notification too large")
		}
		return payload, false, err
	}
	var raw bertlv.TLV
	if err = raw.UnmarshalBinary(request.Payload); err != nil {
		return nil, false, err
	}
	var selected sgp22.PendingNotification
	if err = selected.UnmarshalBERTLV(bertlv.NewChildren(bertlv.ContextSpecific.Constructed(0), &raw)); err != nil {
		return nil, false, err
	}
	if selected.Notification == nil || notificationEntryFromPending(&selected) != *request.Expected {
		return nil, false, errNotificationChanged
	}
	receiver, err := notificationReceiverURL(request.Expected.Address)
	if err != nil {
		return nil, false, err
	}
	if err = sendPendingNotification(ctx, client, receiver, &selected); err != nil {
		return nil, false, err
	}
	return nil, true, nil
}
