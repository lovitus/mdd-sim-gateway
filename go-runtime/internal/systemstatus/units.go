package systemstatus

import "context"

type UnitSource interface {
	CollectUnits(context.Context) Section[SystemdInfo]
}
