//go:build !linux

package systemstatus

import "context"

type unsupportedUnitSource struct{}

func newUnitSource() UnitSource { return unsupportedUnitSource{} }

func (unsupportedUnitSource) CollectUnits(context.Context) Section[SystemdInfo] {
	return unavailableSection[SystemdInfo]("systemd_unsupported")
}
