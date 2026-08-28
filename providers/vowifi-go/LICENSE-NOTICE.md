# License notice

This directory is an independently built MDD provider module licensed under
`AGPL-3.0-only`. It links the following upstream module:

- `github.com/boa-z/vowifi-go`
- pinned version: `v0.0.0-20260709161034-1e9c6e6adbfc`
- exact commit: `1e9c6e6adbfcd9667695149d5ecb0f71cd062f07`
- upstream source: <https://github.com/boa-z/vowifi-go>
- upstream license: GNU Affero General Public License v3.0

Any deployed network service built from this module must make the complete
corresponding source for this provider and its AGPL dependency available to
its users as required by the AGPL. This module is kept behind a process
boundary; the MDD Core module does not import it.

The complete AGPL text must be included in release packaging before this
provider is distributed or deployed. This development slice is designed for
an eventual process boundary, but does not yet contain or deploy that service.
