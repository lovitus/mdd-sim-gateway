# Current third-party software and retained attribution

This is a human-readable inventory, not a replacement for the exact module lockfiles, distributed license texts,
or corresponding-source archives. Current Go releases retain dependency licenses and the exact Provider source;
all derivative-work copyright and permission notices remain applicable. Physical process separation alone is not
used here as proof of license compliance.

## Current source/release dependencies and lineage

| Component | Use | License | Source |
|---|---|---|---|
| damonto/euicc-go v1.1.2 | eUICC protocol and LPA; minimal optional-IMEI compatibility patch in `go-runtime/third_party/euicc-go/MDD-PATCH.md` | MIT | https://github.com/damonto/euicc-go |
| MddIdd/mdd-sim-gateway | GPL project lineage and substantial original gateway code | GPL-3.0-only | https://github.com/MddIdd/mdd-sim-gateway |
| MDD VoWiFi Go Provider (`providers/vowifi-go/**`) | Native VoWiFi runtime and its maintained upstream fork | AGPL-3.0-only | https://github.com/lovitus/mdd-sim-gateway/tree/main/providers/vowifi-go |
| pagecat/vowifi_gateway | Upstream project this gateway derives from: control-plane, engine and WebUI architecture and substantial code | MIT | https://github.com/pagecat/vowifi_gateway |
| SagerNet/sing-box | Country-specific network exits | GPL-3.0-or-later | https://github.com/SagerNet/sing-box |
| SagerNet/sing-usbip (`v0.0.0-20260831204559-463a80475917`) | Windows/Linux raw USB/IP exporter/importer transport, pinned to `463a80475917` by the exact replacement in `go-runtime/go.mod` | GPL-3.0-or-later | https://github.com/lovitus/sing-usbip (fork of SagerNet) |
| SagerNet/sing-mux (`v0.3.5`) | Multiplexes all raw USB/IP logical connections inside one authenticated WSS session | GPL-3.0-or-later | https://github.com/SagerNet/sing-mux |
| LudovicRousseau/PCSC | PC/SC middleware | BSD-3-Clause | https://github.com/LudovicRousseau/PCSC |
| LudovicRousseau/CCID | USB smart-card driver | LGPL-2.1-or-later | https://github.com/LudovicRousseau/CCID |
| libusb 1.0.30 | Static USB transport used by the macOS cellular companion | LGPL-2.1-or-later | https://github.com/libusb/libusb |
| lwIP 2.2.1 | Private TCP/IP stack used by the macOS cellular companion | BSD-3-Clause | https://github.com/lwip-tcpip/lwip |
| jsQR | QR decoding for eSIM activation codes | Apache-2.0 | https://github.com/cozmo/jsQR |
| React | Web interface | MIT | https://github.com/facebook/react |
| Tailwind CSS | Web interface styling | MIT | https://github.com/tailwindlabs/tailwindcss |
| Twemoji Mozilla | Bundled color Emoji font used for country flags in proxy node names | Apache-2.0 (font tooling/code); Twemoji artwork CC-BY-4.0 | https://github.com/mozilla/twemoji-colr |
| gen2brain/malgo / miniaudio | Native capture/playback backend for the Agent call-audio helper | Unlicense / public-domain-compatible | https://github.com/gen2brain/malgo |
| opencore-amr | cgo AMR-NB media codec used by the Provider | Apache-2.0 | https://sourceforge.net/projects/opencore-amr/ |
| mobile-broadband-provider-info | Embedded APN advisory data; dedication retained in XML | Public domain | https://gitlab.gnome.org/GNOME/mobile-broadband-provider-info |

The table is a summary. Exact Go versions are in go.mod/go.sum; npm versions in webui/package-lock.json;
macOS native helper pins in agent/cellular-io/THIRD_PARTY.md. Distribution PC/SC/CCID dependencies remain subject
to their own terms. Twemoji Mozilla is Copyright 2016–2018 Mozilla Foundation; its Twemoji visual artwork is
Copyright Twitter, Inc. and contributors (CC-BY-4.0). The unmodified font/associated notices remain in the UI assets.

## Retired dependencies and historical notices

Python Control, VPCD, Asterisk/pjproject, panoramisk, PyCryptodome, pyscard, FastAPI, JsSIP, lpac,
the old AOSP carrier table and proprietary codec_opus integration are not current build/install entrypoints.
Their historical inventory and attribution are preserved in
[the archived notices](docs/archive/2026-09-20/THIRD_PARTY_LICENSES.md) and
[NOTICE](docs/archive/2026-09-20/NOTICE), not silently erased. This classification does not authorize
redistribution of historical proprietary modules or relicense old derivative files.
Retired sources are recoverable from the pinned Git history in [data provenance](docs/data-provenance.md).

## Retained upstream notice: pagecat/vowifi_gateway (MIT)

MDD Sim Gateway is a derivative work of
[pagecat/vowifi_gateway](https://github.com/pagecat/vowifi_gateway), which contributes the VoWiFi
engine and the overall control-plane, engine and WebUI architecture. MDD Sim Gateway adds 4G
cellular data and SMS, per-country network egress routing, unified device management and automatic
provisioning, failover and a test suite. The combined work is distributed under GPL-3.0-only as
permitted by the MIT license; the original copyright and permission notice is retained below as
the MIT license requires:

```
MIT License

Copyright (c) 2026 pagecat

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

## Independently licensed paths and release obligations

`providers/vowifi-go/**` remains AGPL-3.0-only; `go-runtime/third_party/euicc-go/**` retains MIT terms;
font, data and native/driver components retain their original notices. Provider releases must continue to include
the exact corresponding-source archive and providers/vowifi-go/LICENSE-NOTICE.md and support applicable network
source-offer obligations. Nothing in this cleanup changes those duties.

Historical Asterisk derivative files remain GPL-2.0-only in Git history. Their full license text is preserved in
[docs/archive/licenses/asterisk-GPL-2.0.txt](docs/archive/licenses/asterisk-GPL-2.0.txt). They are no longer patched,
compiled or installed by current release scripts. The old proprietary codec module must not be included without
separate appropriate redistribution permission.

Windows sing-usbip-based releases retain VBoxUSB/usbip-win2 driver-specific GPL-3.0-only/BSD-2-Clause notices under
THIRD-PARTY-LICENSES/sing-usbip-windows-drivers and the exact pinned source provenance. Do not remove generated
license or Provider-source manifest entries merely because legacy runtime files were removed.
