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
	card, err := context.Connect(readerName, scard.ShareShared, scard.ProtocolAny)
	if err != nil {
		_ = context.Release()
		return nil, err
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
