# License notice

This directory is an independently built MDD provider module licensed under
`AGPL-3.0-only`. It contains and links the following upstream module:

- `github.com/boa-z/vowifi-go`
- pinned version: `v0.0.0-20260709161034-1e9c6e6adbfc`
- exact commit: `1e9c6e6adbfcd9667695149d5ecb0f71cd062f07`
- upstream source: <https://github.com/boa-z/vowifi-go>
- upstream license: GNU Affero General Public License v3.0

The complete reviewed upstream source snapshot and its full AGPL license are
included under `upstream/vowifi-go`. `UPSTREAM.md` records the narrow MDD patch.

The userspace socket adapter uses the MIT-licensed
`golang.zx2c4.com/wireguard/tun/netstack` package, pinned through
`golang.zx2c4.com/wireguard` version
`v0.0.0-20260522210424-ecfc5a8d5446`. Only its in-memory gVisor adapter is
used; no WireGuard tunnel, key management, OS TUN, or route management is used.

Any deployed network service built from this module must make the complete
corresponding source for this provider and its AGPL dependency available to
its users as required by the AGPL. This module is kept behind a process
boundary; the MDD Core module does not import it.

Release packaging must retain the complete provider/upstream source, license
and notices and satisfy the AGPL network-source obligation. This development
slice is designed for an eventual process boundary, but does not yet contain
or deploy that service.
