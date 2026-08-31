# Third-party software notices

This list covers the material dependencies intentionally used by MDD Sim Gateway. Linux binary
release bundles also contain a generated archive of exact Go dependency licenses, notices and any
source that their licenses require; source checkouts retain the corresponding package distributions.

| Component | Use | License | Source |
|---|---|---|---|
| MddIdd/mdd-sim-gateway | GPL project lineage and substantial original gateway code | GPL-3.0-only | https://github.com/MddIdd/mdd-sim-gateway |
| MDD VoWiFi Go Provider (`providers/vowifi-go/**`) | Native VoWiFi runtime and its maintained upstream fork | AGPL-3.0-only | https://github.com/lovitus/mdd-sim-gateway/tree/main/providers/vowifi-go |
| pagecat/vowifi_gateway | Upstream project this gateway derives from: control-plane, engine and WebUI architecture and substantial code | MIT | https://github.com/pagecat/vowifi_gateway |
| fasferraz/SWu-IKEv2 | Modified SWu IKEv2/IPsec engine | GPL-3.0 | https://github.com/fasferraz/SWu-IKEv2 |
| sysmocom/Asterisk | IMS-AKA SIP, voice and SMS | GPL-2.0-only | https://gitea.sysmocom.de/sysmocom/asterisk |
| sysmocom/pjproject | SIP stack built into the engine's Asterisk; selected under GPL-2.0 when linked there | GPL-2.0-or-later | https://gitea.sysmocom.de/sysmocom/pjproject |
| Sangoma/Digium codec_opus binary module | Optional proprietary Opus transcoder downloaded by the legacy Engine build; not part of the GPL source and not redistributable without separate permission | Proprietary EULA | https://downloads.asterisk.org/pub/telephony/codec_opus/ |
| phcoder/asterisk-docker | Reference build/integration | MIT | https://github.com/phcoder/asterisk-docker |
| mitshell/card | USIM and PC/SC helpers | GPL-2.0-or-later | https://github.com/mitshell/card |
| SagerNet/sing-box | Country-specific network exits | GPL-3.0-or-later | https://github.com/SagerNet/sing-box |
| SagerNet/sing-usbip (`v0.0.0-20260817040617-28bd42667eca`) | Windows/Linux raw USB/IP exporter/importer transport, pinned to commit `28bd42667ecac597d3e19492b9e81ee04e22792f` | GPL-3.0-or-later | https://github.com/SagerNet/sing-usbip |
| SagerNet/sing-mux (`v0.3.5`) | Multiplexes all raw USB/IP logical connections inside one authenticated WSS session | GPL-3.0-or-later | https://github.com/SagerNet/sing-mux |
| estkme-group/lpac | Local eSIM profile assistant | AGPL-3.0-only | https://github.com/estkme-group/lpac |
| LudovicRousseau/PCSC | PC/SC middleware | BSD-3-Clause | https://github.com/LudovicRousseau/PCSC |
| LudovicRousseau/CCID | USB smart-card driver | LGPL-2.1-or-later | https://github.com/LudovicRousseau/CCID |
| libusb 1.0.30 | Static USB transport used by the macOS cellular companion | LGPL-2.1-or-later | https://github.com/libusb/libusb |
| lwIP 2.2.1 | Private TCP/IP stack used by the macOS cellular companion | BSD-3-Clause | https://github.com/lwip-tcpip/lwip |
| frankmorgner/vsmartcard (vpcd) | Virtual PC/SC driver backing the cellular modem's SIM slots | GPL-3.0 | https://github.com/frankmorgner/vsmartcard |
| pyscard | PC/SC smart-card access | LGPL-2.1-or-later | https://github.com/LudovicRousseau/pyscard |
| PyCryptodome | IKEv2/ESP cryptography in the SWu engine | BSD-2-Clause and Public Domain | https://github.com/Legrandin/pycryptodome |
| panoramisk | Asterisk AMI client | MIT | https://github.com/gawel/panoramisk |
| JsSIP | Browser SIP/WebRTC client | MIT | https://github.com/versatica/JsSIP |
| jsQR | QR decoding for eSIM activation codes | Apache-2.0 | https://github.com/cozmo/jsQR |
| React | Web interface | MIT | https://github.com/facebook/react |
| Tailwind CSS | Web interface styling | MIT | https://github.com/tailwindlabs/tailwindcss |
| Twemoji Mozilla | Bundled color Emoji font used for country flags in proxy node names | Apache-2.0 (font tooling/code); Twemoji artwork CC-BY-4.0 | https://github.com/mozilla/twemoji-colr |
| FastAPI | Control API framework | MIT | https://github.com/fastapi/fastapi |
| Android Open Source Project Carrier ID table | Offline MNO/MVNO identification data | Apache-2.0 | https://android.googlesource.com/platform/packages/providers/TelephonyProvider/ |
| gen2brain/malgo / miniaudio | Native capture/playback backend for the Agent call-audio helper | Unlicense / public-domain-compatible | https://github.com/gen2brain/malgo |

Twemoji Mozilla is built by Mozilla from Twemoji artwork. The font project is Copyright
2016-2018 Mozilla Foundation and licensed under Apache-2.0; the embedded visual designs are
Copyright Twitter, Inc. and other contributors and licensed under CC-BY-4.0. The bundled font is
unmodified and sourced from the same upstream font used by Clash Verge Rev.

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

## Files that are not GPL-3.0-only

MDD Sim Gateway defaults to GPL-3.0-only, but some files are derivative works of
upstream projects and keep the upstream license instead. Differently licensed derivative
code cannot simply be relicensed to GPL-3.0-only, so these paths are tracked explicitly:

| Path | License | Derived from |
|---|---|---|
| `providers/vowifi-go/**` | AGPL-3.0-only | Native Provider and maintained `boa-z/vowifi-go` fork |
| `engine/patches/asterisk/**` | GPL-2.0-only | sysmocom/Asterisk and its chan_websocket backport |

The VoWiFi Provider is a separate Go module and process. Its AGPL-3.0-only license requires
that users interacting with a modified Provider over a network be offered its complete
corresponding source. Linux release bundles therefore include the exact Provider source archive
and `providers/vowifi-go/LICENSE-NOTICE.md` for the built revision.

The full GPL-2.0-only terms for the Asterisk derivative files are retained at
`engine/patches/asterisk/LICENSE`; each maintained patch source carries an SPDX identifier.

Those files patch the upstream source at build time. The patched Asterisk runs as a separate
process inside the legacy engine container and communicates with the GPL-3.0-only control
plane over AMI and HTTP only. No GPL-3.0-only code is linked into it. Redistributing a built
image or host install means also offering the corresponding modified Asterisk source under
GPL-2.0-only.

The legacy engine image builds sysmocom's pjproject (GPL-2.0-or-later) from a pinned commit,
selects GPL-2.0 for compatibility, and links it into that same Asterisk binary. Shipping a
built engine image therefore also requires offering the corresponding pjproject source.
Nothing in this repository is derived from pjproject; it is fetched and compiled unmodified.

The same legacy build enables Asterisk's binary-module downloader for `codec_opus.so`.
Sangoma's accompanying EULA does not grant general sublicensing or redistribution rights.
Public or customer release artifacts must therefore exclude that proprietary module unless
the distributor has obtained separate written redistribution permission. A local user build
may download it only to the extent allowed by the user's own agreement with Sangoma.

The virtual PC/SC driver that backs a cellular modem's SIM slots (`libifdvpcd.so` from
frankmorgner/vsmartcard, GPL-3.0) is installed from the distribution's `vsmartcard-vpcd` package
by `install.sh` and loaded by pcscd as a separate component. `host/vpcd_modem_bridge.py` is
original GPL-3.0-only code that speaks the VPCD wire protocol to it; no vsmartcard code is copied
into this repository.

MDD does not copy sing-box or lpac binaries into this source repository. The installer fetches pinned upstream releases/source and verifies published or reviewed SHA-256 values where a binary is downloaded. Full license texts and copyright notices are included in those upstream distributions.

Windows Agent binaries built with sing-usbip embed its upstream VBoxUSB exporter and usbip-win2
importer driver assets. The Windows release package reproduces the driver-specific GPL-3.0-only
and BSD-2-Clause notices under `THIRD-PARTY-LICENSES/sing-usbip-windows-drivers`; the exact
corresponding source is the sing-usbip commit pinned in the table above and in `go-runtime/go.mod`.
