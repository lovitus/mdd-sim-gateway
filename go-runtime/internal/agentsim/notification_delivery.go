package agentsim

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/damonto/euicc-go/lpa"
	sgp22 "github.com/damonto/euicc-go/v2"
	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
)

var (
	errNotificationChanged          = errors.New("eUICC notification changed")
	errNotificationNotFound         = errors.New("eUICC notification not found")
	errNotificationAddress          = errors.New("eUICC notification receiver is invalid")
	errNotificationReceiverRejected = errors.New("eUICC notification receiver rejected delivery")
	errNotificationOutcomeUnknown   = errors.New("eUICC notification delivery outcome is unknown")
)

// deliverEUICCNotification implements the SGP.22 ordering for exactly one
// retained notification: retrieve, send, wait for the receiver's HTTP 204
// acknowledgement, then remove it from the eUICC. It never retries the network
// POST because a transport failure may happen after the receiver accepted it.
func deliverEUICCNotification(ctx context.Context, card Card, aid []byte,
	expected agentlink.EUICCNotificationEntry) (acknowledged bool, removed bool, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("eUICC library panic: %v", recovered)
		}
	}()
	client, err := lpa.New(&lpa.Options{
		Channel: &euiccCardChannel{ctx: ctx, card: card}, AID: append([]byte(nil), aid...),
		Timeout: 90 * time.Second, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		return false, false, err
	}
	defer func() {
		closeErr := client.Close()
		if !removed {
			err = errors.Join(err, closeErr)
		}
	}()
	return deliverEUICCNotificationWithClient(ctx, client, expected)
}

func deliverEUICCNotificationWithClient(ctx context.Context, client *lpa.Client,
	expected agentlink.EUICCNotificationEntry) (acknowledged bool, removed bool, err error) {
	selected, err := retrieveExpectedNotification(client, expected)
	if err != nil {
		return false, false, err
	}
	receiver, err := notificationReceiverURL(expected.Address)
	if err != nil {
		return false, false, errors.Join(errNotificationAddress, err)
	}
	if err := sendPendingNotification(ctx, client, receiver, selected); err != nil {
		return false, false, err
	}
	acknowledged = true
	if err := client.RemoveNotificationFromList(sgp22.SequenceNumber(expected.SequenceNumber)); err != nil && !errors.Is(err, sgp22.ErrNothingToDelete) {
		return true, false, err
	}
	return true, true, nil
}

// removeEUICCNotification is the recovery half of a delivery that the remote
// receiver already acknowledged. It performs no HTTP request. The current
// card entry must still match the complete delivery intent before removal.
func removeEUICCNotification(ctx context.Context, card Card, aid []byte,
	expected agentlink.EUICCNotificationEntry) (removed bool, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("eUICC library panic: %v", recovered)
		}
	}()
	client, err := lpa.New(&lpa.Options{
		Channel: &euiccCardChannel{ctx: ctx, card: card}, AID: append([]byte(nil), aid...),
		Timeout: 90 * time.Second, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		return false, err
	}
	defer func() {
		closeErr := client.Close()
		if !removed {
			err = errors.Join(err, closeErr)
		}
	}()
	if _, err := retrieveExpectedNotification(client, expected); errors.Is(err, errNotificationNotFound) {
		return true, nil
	} else if err != nil {
		return false, err
	}
	if err := client.RemoveNotificationFromList(sgp22.SequenceNumber(expected.SequenceNumber)); err != nil &&
		!errors.Is(err, sgp22.ErrNothingToDelete) {
		return false, err
	}
	return true, nil
}

func retrieveExpectedNotification(client *lpa.Client,
	expected agentlink.EUICCNotificationEntry) (*sgp22.PendingNotification, error) {
	pending, err := client.RetrieveNotificationList(sgp22.SequenceNumber(expected.SequenceNumber))
	if errors.Is(err, sgp22.ErrUndefined) {
		return nil, errNotificationNotFound
	}
	if err != nil {
		return nil, err
	}
	var selected *sgp22.PendingNotification
	for _, candidate := range pending {
		if candidate == nil || candidate.Notification == nil ||
			int64(candidate.Notification.SequenceNumber) != expected.SequenceNumber {
			continue
		}
		if selected != nil {
			return nil, errNotificationChanged
		}
		selected = candidate
	}
	if selected == nil {
		return nil, errNotificationNotFound
	}
	actual := notificationEntryFromPending(selected)
	if actual.Validate() != nil || actual != expected {
		return nil, errNotificationChanged
	}
	return selected, nil
}

func notificationEntryFromPending(pending *sgp22.PendingNotification) agentlink.EUICCNotificationEntry {
	metadata := pending.Notification
	return agentlink.EUICCNotificationEntry{
		SequenceNumber: int64(metadata.SequenceNumber),
		Event:          notificationEventName(metadata.ProfileManagementOperation),
		ICCID:          metadata.ICCID.String(), Address: strings.TrimSpace(metadata.Address),
	}
}

func notificationReceiverURL(address string) (*url.URL, error) {
	address = strings.TrimSpace(address)
	if address == "" || strings.Contains(address, "://") || strings.ContainsAny(address, "/?#@") {
		return nil, errNotificationAddress
	}
	candidate, err := url.Parse("https://" + address)
	if err != nil || candidate.Hostname() == "" || candidate.User != nil || candidate.Path != "" ||
		candidate.RawQuery != "" || candidate.Fragment != "" {
		return nil, errNotificationAddress
	}
	if port := candidate.Port(); port != "" {
		value, parseErr := strconv.Atoi(port)
		if parseErr != nil || value < 1 || value > 65535 {
			return nil, errNotificationAddress
		}
	}
	host := strings.TrimSuffix(strings.ToLower(candidate.Hostname()), ".")
	if host == "localhost" {
		return nil, errNotificationAddress
	}
	if address := net.ParseIP(host); address != nil &&
		(address.IsLoopback() || address.IsPrivate() || address.IsUnspecified() || address.IsLinkLocalUnicast()) {
		return nil, errNotificationAddress
	}
	candidate.Path = ""
	return candidate, nil
}

func sendPendingNotification(ctx context.Context, client *lpa.Client, receiver *url.URL,
	pending *sgp22.PendingNotification) error {
	payload, err := json.Marshal(&sgp22.ES9HandleNotificationRequest{
		PendingNotification: pending.PendingNotification,
	})
	if err != nil {
		return err
	}
	endpoint := receiver.JoinPath("/gsma/rsp2/es9plus/handleNotification")
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header = client.HTTP.Header()
	httpClient := *client.HTTP.Client
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := httpClient.Do(request)
	if err != nil {
		return errors.Join(errNotificationOutcomeUnknown, err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("%w: HTTP %d", errNotificationReceiverRejected, response.StatusCode)
	}
	return nil
}
