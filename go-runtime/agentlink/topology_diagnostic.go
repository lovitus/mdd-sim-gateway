package agentlink

import "errors"

// topologyValidationError preserves the stable rule description while adding
// a schema path composed only of fixed field names and numeric indices. Raw
// reader names, identities, PINs, APNs and topology values must never appear.
type topologyValidationError struct {
	field string
	rule  string
}

func (err *topologyValidationError) Error() string { return err.rule }

func topologyInvalid(field, rule string) error {
	return &topologyValidationError{field: field, rule: rule}
}

// TopologyValidationField returns an additive diagnostic, not an admission
// override. A broad legacy rule stays broad instead of inventing a field.
func TopologyValidationField(err error) string {
	var invalid *topologyValidationError
	if errors.As(err, &invalid) {
		return invalid.field
	}
	return "topology"
}
