# License notice

The MDD Core embedded browser UI includes the zero-dependency `qr` package
version `0.6.0` from <https://github.com/paulmillr/qr>, used under the Apache
License 2.0. Its exact decoder modules, provenance and full license are retained
under `go-runtime/internal/webui/assets/qr` and are included in the complete
source archive shipped with each release.

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

The PCM/G.711 media boundary uses `github.com/zaf/g711` v1.4.0 under its
BSD 3-Clause License. Release packaging must retain its copyright, license
conditions and disclaimer.

The AMR-NB media boundary statically links OpenCORE AMR version 0.1.6 from the
Debian `libopencore-amrnb-dev` package. OpenCORE AMR implements 3GPP TS 26.073,
is derived from the Android OpenCORE framework, and is licensed under the
Apache License 2.0. Its full license is retained at
`licenses/opencore-amr-LICENSE`. Relevant upstream attribution retained from
the OpenCORE notice:

- Copyright (C) 2009 PacketVideo.
- Portions are derived from 3GPP TS 26.073, ANSI-C code for the AMR speech
  codec; Copyright (C) 2004, 3GPP Organizational Partners (ARIB, ATIS, CCSA,
  ETSI, TTA, TTC).

MDD does not reimplement the AMR codec algorithm. It calls OpenCORE through its
published narrowband interface and implements only the single-frame RTP
payload layout specified by RFC 4867.

The optional SOCKS5 UDP transport uses the MIT-licensed
`github.com/txthinking/socks5` package, pinned at
`v0.0.0-20260601051520-339b044ab0eb`. Its license is retained at
`licenses/txthinking-socks5-LICENSE` and must remain in provider source
packages.

Any deployed network service built from this module must make the complete
corresponding source for this provider and its AGPL dependency available to
its users as required by the AGPL. This module is kept behind a process
boundary; the MDD Core module does not import it.

Release packaging must retain the complete provider/upstream source, license
and notices and satisfy the AGPL network-source obligation. The deployed
Provider remains an isolated process; the MDD Core module does not import or
link this AGPL module.
