package agentlink

import "strings"

// EUICCSoftDeleteMarker is removable only through an explicit nickname edit.
// It is a local management guard, not an operator deletion acknowledgement.
const EUICCSoftDeleteMarker = "[MDD-DELETED]"

func EUICCProfileSoftDeleted(nickname string) bool {
	return strings.Contains(nickname, EUICCSoftDeleteMarker)
}
