package agentsim

import (
	"context"
	"errors"
	"strings"

	"github.com/lovitus/mdd-sim-gateway/go-runtime/agentlink"
)

func readReaderSIMIdentity(ctx context.Context, card Card) agentlink.ReaderSIMFact {
	// The read sequence is ported from MDD control/app/sim.py and then
	// hardened with the VoHive/VoCat implementations cited beside the
	// individual decoders below. It stays inside the existing card session.
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
	if administrative, readErr := readTransparentEF(ctx, card, 0x6FAD, 4); readErr == nil {
		if length, valid := mncLengthFromAD(administrative); valid && len(imsi) >= 3+length {
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
	// Adapted directly from boa-z/vowifi-go 1e9c6e6a
	// runtimehost/simauth.DecodeUSIMIMSI. Keep the full
	// mobile-identity type, parity and filler checks rather than only swapping
	// nibbles as the retired MDD Python helper did.
	if len(data) == 0 {
		return "", errors.New("EF_IMSI data is empty")
	}
	length := int(data[0])
	if length <= 0 || len(data)-1 < length {
		return "", errors.New("EF_IMSI payload length is invalid")
	}
	mobileID := data[1 : 1+length]
	if len(mobileID) == 0 || mobileID[0]&0x07 != 0x01 {
		return "", errors.New("EF_IMSI mobile identity type is not IMSI")
	}
	oddDigits := mobileID[0]&0x08 != 0
	digits := make([]byte, 0, 1+2*(len(mobileID)-1))
	if !appendBCDDigit(&digits, mobileID[0]>>4) {
		return "", errors.New("EF_IMSI digit 1 is not BCD")
	}
	for index, value := range mobileID[1:] {
		if !appendBCDDigit(&digits, value&0x0F) {
			return "", errors.New("EF_IMSI contains invalid BCD")
		}
		high := value >> 4
		last := index == len(mobileID[1:])-1
		if last && !oddDigits {
			if high != 0x0F {
				return "", errors.New("EF_IMSI even-length filler is invalid")
			}
			continue
		}
		if !appendBCDDigit(&digits, high) {
			return "", errors.New("EF_IMSI contains invalid BCD")
		}
	}
	if oddDigits && len(digits)%2 == 0 || !oddDigits && len(digits)%2 != 0 || len(digits) < 5 || len(digits) > 15 {
		return "", errors.New("EF_IMSI odd/even indicator or length is invalid")
	}
	return string(digits), nil
}

func appendBCDDigit(target *[]byte, digit byte) bool {
	if digit > 9 {
		return false
	}
	*target = append(*target, '0'+digit)
	return true
}

// Adapted from boa-z/vowifi-go 1e9c6e6a runtimehost/simauth.MNCLengthFromAD.
func mncLengthFromAD(data []byte) (int, bool) {
	if len(data) < 4 {
		return 0, false
	}
	length := int(data[3] & 0x0F)
	return length, length == 2 || length == 3
}

func readSMSP(ctx context.Context, card Card) (string, error) {
	selected, err := exchange(ctx, card, []byte{0x00, 0xA4, 0x00, 0x04, 0x02, 0x6F, 0x42, 0x00})
	if err != nil {
		return "", err
	}
	if !selected.success() {
		return "", &apduStatusError{"select EF_SMSP", selected.sw1, selected.sw2}
	}
	recordLength := linearFixedRecordLength(selected.body)
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

// Adapted from VoHive 35ba2a2 internal/simaid.ParseLinearFixedMetaFromFCP.
// The file descriptor stores record length in the two octets before the
// record-count octet. Accept the older four-octet form seen by legacy cards.
func linearFixedRecordLength(fcp []byte) int {
	for _, descriptor := range findTLVValues(fcp, 0x82) {
		switch {
		case len(descriptor) >= 5:
			return int(descriptor[len(descriptor)-3])<<8 | int(descriptor[len(descriptor)-2])
		case len(descriptor) == 4:
			return int(descriptor[2])<<8 | int(descriptor[3])
		}
	}
	return 0
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
