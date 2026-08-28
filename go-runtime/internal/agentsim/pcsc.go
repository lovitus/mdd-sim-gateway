package agentsim

import (
	"errors"

	"github.com/ebfe/scard"
)

type PCSCConnector struct{}

type pcscCard struct {
	context *scard.Context
	card    *scard.Card
}

func (PCSCConnector) Connect(readerName string) (Card, error) {
	context, err := scard.EstablishContext()
	if err != nil {
		return nil, err
	}
	var card *scard.Card
	var failures []error
	for _, protocol := range []scard.Protocol{scard.ProtocolT0, scard.ProtocolT1, scard.ProtocolAny} {
		card, err = context.Connect(readerName, scard.ShareShared, protocol)
		if err == nil {
			break
		}
		failures = append(failures, err)
	}
	if card == nil {
		_ = context.Release()
		return nil, errors.Join(failures...)
	}
	return &pcscCard{context: context, card: card}, nil
}

func (card *pcscCard) BeginTransaction() error {
	return card.card.BeginTransaction()
}

func (card *pcscCard) EndTransaction() error {
	return card.card.EndTransaction(scard.LeaveCard)
}

func (card *pcscCard) Transmit(command []byte) ([]byte, error) {
	return card.card.Transmit(command)
}

func (card *pcscCard) Close() error {
	return errors.Join(card.card.Disconnect(scard.LeaveCard), card.context.Release())
}
