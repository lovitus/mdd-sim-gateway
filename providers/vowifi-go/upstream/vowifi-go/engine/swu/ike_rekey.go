package swu

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/boa-z/vowifi-go/engine/swu/ikev2"
)

var ErrIKESAReplacement = errors.New("IKE-SA replacement or retirement is unconfirmed")

type IKESARekeyScheduler interface {
	NextIKESARekeyDue() (time.Time, bool)
	RunIKESARekeyDue(context.Context, time.Time) error
}

func (s *PacketSession) NextIKESARekeyDue() (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nextIKERekey, !s.closed && !s.nextIKERekey.IsZero() && s.rekeyFailure == nil
}

func (s *PacketSession) RunIKESARekeyDue(ctx context.Context, now time.Time) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.rekeyMu.Lock()
	defer s.rekeyMu.Unlock()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrPacketTunnelClosed
	}
	if s.rekeyFailure != nil {
		err := s.rekeyFailure
		s.mu.Unlock()
		return err
	}
	if s.nextIKERekey.IsZero() || now.Before(s.nextIKERekey) {
		s.mu.Unlock()
		return nil
	}
	if s.previousInbound != nil && (s.previousUntil.IsZero() || time.Now().Before(s.previousUntil)) {
		s.mu.Unlock()
		return ErrChildSAOverlap
	}
	handler := s.ikeRekeyHandler
	s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if handler == nil {
		return ErrInvalidIKEControl
	}
	if err := handler(ctx); err != nil {
		if errors.Is(err, ikev2.ErrExchangeUncertain) {
			return s.abortSAReplacement(errors.Join(ErrIKESAReplacement, err), "ike sa retirement unconfirmed")
		}
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrPacketTunnelClosed
	}
	s.nextIKERekey = now.Add(s.ikeRekeyLifetime)
	return nil
}

func (c *ikePacketTunnelControl) reserveMessageID() (uint32, error) {
	if c.messageIDExhausted {
		return 0, fmt.Errorf("%w: exhausted message IDs", ErrInvalidIKEControl)
	}
	id := c.nextMessageID
	if id == ^uint32(0) {
		c.messageIDExhausted = true
	} else {
		c.nextMessageID++
	}
	return id, nil
}

func (c *ikePacketTunnelControl) rekeyIKE(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pendingChild != nil {
		return ErrChildSAOverlap
	}
	if c.ikeInstalled == nil || c.ikeRetired == nil {
		return ErrInvalidIKEControl
	}
	id, err := c.reserveMessageID()
	if err != nil {
		return err
	}
	observed := &childRekeyTransport{InitTransport: ikev2.RetransmitTransport{Transport: c.transport}}
	next, err := ikev2.RunIKESARekey(ctx, observed, c.init, id, c.random)
	if err != nil {
		var rejected *ikev2.NotifyError
		if observed.attempted && !errors.As(err, &rejected) {
			return errors.Join(ikev2.ErrExchangeUncertain, err)
		}
		return err
	}
	previous := c.init
	deleteID, err := c.reserveMessageID()
	if err != nil {
		return errors.Join(ikev2.ErrExchangeUncertain, err)
	}
	if err = c.ikeInstalled(previous, next); err != nil {
		return errors.Join(ikev2.ErrExchangeUncertain, err)
	}
	c.init, c.keys = next, next.Keys
	c.nextMessageID, c.messageIDExhausted = 0, false
	result, err := ikev2.RunInformationalExchange(ctx, ikev2.InformationalConfig{Transport: ikev2.RetransmitTransport{Transport: c.transport}, Init: previous, Keys: previous.Keys, MessageID: deleteID, Payloads: []ikev2.Payload{ikev2.IKEDeletePayload()}, Random: c.random})
	if err != nil {
		return errors.Join(ikev2.ErrExchangeUncertain, err)
	}
	if result.Response.NotifyError != nil || len(result.Response.Deletes) != 0 {
		return errors.Join(ikev2.ErrExchangeUncertain, errors.New("invalid old IKE DELETE acknowledgement"), result.Response.NotifyError)
	}
	c.ikeRetired(previous)
	return nil
}
