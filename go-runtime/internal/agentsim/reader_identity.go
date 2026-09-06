package agentsim

import (
	"context"
	"errors"
	"strings"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
)

func readReaderSIMIdentity(ctx context.Context, card Card) agentlink.ReaderSIMFact {
	fact := agentlink.ReaderSIMFact{IdentityState: "unavailable", ErrorCode: "reader_sim_application_unavailable"}
	if err := selectApplication(ctx, card, agentlink.AKAApplicationUSIM); err != nil {
		return fact
	}
	imsiData, err := readTransparentEF(ctx, card, 0x6F07, 9)
	if err != nil {
		var status *apduStatusError
		if errors.As(err, &status) && status.sw1 == 0x69 && status.sw2 == 0x82 {
			fact.IdentityState = "pin_required"
			fact.ErrorCode = "reader_sim_pin_required"
		} else {
			fact.ErrorCode = "reader_sim_imsi_unavailable"
		}
		return fact
	}
	imsi, err := decodeIMSI(imsiData)
	if err != nil {
		fact.ErrorCode = "reader_sim_imsi_invalid"
		return fact
	}
	fact.IMSI, fact.MCC = imsi, imsi[:3]
	fact.IdentityState = "partial"
	fact.ErrorCode = "reader_sim_mnc_length_unavailable"
	if administrative, readErr := readTransparentEF(ctx, card, 0x6FAD, 4); readErr == nil && len(administrative) >= 4 {
		length := int(administrative[3] & 0x0F)
		if (length == 2 || length == 3) && len(imsi) >= 3+length {
			fact.MNC = imsi[3 : 3+length]
			fact.IdentityState = "ready"
			fact.ErrorCode = ""
		}
	}
	if smsc, readErr := readSMSP(ctx, card); readErr == nil {
		fact.SMSC = smsc
	}
	return fact
}

func readTransparentEF(ctx context.Context, card Card, fileID uint16, length byte) ([]byte, error) {
	selected, err := exchange(ctx, card, []byte{0x00, 0xA4, 0x00, 0x04, 0x02, byte(fileID >> 8), byte(fileID), 0x00})
	if err != nil {
		return nil, err
	}
	if !selected.success() {
		return nil, &apduStatusError{"select transparent EF", selected.sw1, selected.sw2}
	}
	read, err := exchange(ctx, card, []byte{0x00, 0xB0, 0x00, 0x00, length})
	if err != nil {
		return nil, err
	}
	if !read.success() {
		return nil, &apduStatusError{"read transparent EF", read.sw1, read.sw2}
	}
	if length != 0 && len(read.body) < int(length) {
		return nil, errors.New("transparent EF response is short")
	}
	return append([]byte(nil), read.body...), nil
}

func decodeIMSI(data []byte) (string, error) {
	if len(data) < 2 {
		return "", errors.New("EF_IMSI is short")
	}
	payloadLength := int(data[0])
	if payloadLength < 3 || payloadLength > len(data)-1 {
		return "", errors.New("EF_IMSI payload length is invalid")
	}
	var digits strings.Builder
	for _, value := range data[1 : 1+payloadLength] {
		for _, digit := range []byte{value & 0x0F, value >> 4} {
			if digit == 0x0F {
				continue
			}
			if digit > 9 {
				return "", errors.New("EF_IMSI contains invalid BCD")
			}
			digits.WriteByte('0' + digit)
		}
	}
	value := digits.String()
	if len(value) < 6 {
		return "", errors.New("EF_IMSI contains too few digits")
	}
	// The first semi-octet contains the parity marker, not an IMSI digit.
	value = value[1:]
	if len(value) < 5 || len(value) > 18 {
		return "", errors.New("EF_IMSI length is invalid")
	}
	return value, nil
}

func readSMSP(ctx context.Context, card Card) (string, error) {
	selected, err := exchange(ctx, card, []byte{0x00, 0xA4, 0x00, 0x04, 0x02, 0x6F, 0x42, 0x00})
	if err != nil {
		return "", err
	}
	if !selected.success() {
		return "", &apduStatusError{"select EF_SMSP", selected.sw1, selected.sw2}
	}
	recordLength := 0
	for _, descriptor := range findTLVValues(selected.body, 0x82) {
		if len(descriptor) >= 4 {
			recordLength = int(descriptor[2])<<8 | int(descriptor[3])
			break
		}
	}
	if recordLength < 28 || recordLength > 255 {
		return "", errors.New("EF_SMSP record length is invalid")
	}
	record, err := exchange(ctx, card, []byte{0x00, 0xB2, 0x01, 0x04, byte(recordLength)})
	if err != nil {
		return "", err
	}
	if !record.success() || len(record.body) < recordLength {
		return "", errors.New("EF_SMSP record is unavailable")
	}
	alphaLength := recordLength - 28
	return decodeTONBCD(record.body[alphaLength+13 : alphaLength+25])
}

func decodeTONBCD(field []byte) (string, error) {
	if len(field) < 2 || field[0] == 0 || field[0] == 0xFF || int(field[0])+1 > len(field) {
		return "", errors.New("address field is empty")
	}
	var digits strings.Builder
	for _, value := range field[2 : 2+int(field[0])-1] {
		for _, digit := range []byte{value & 0x0F, value >> 4} {
			if digit == 0x0F {
				continue
			}
			if digit > 9 {
				return "", errors.New("address field contains invalid BCD")
			}
			digits.WriteByte('0' + digit)
		}
	}
	if digits.Len() == 0 {
		return "", errors.New("address field has no digits")
	}
	prefix := ""
	if field[1]&0x70 == 0x10 {
		prefix = "+"
	}
	return prefix + digits.String(), nil
}
