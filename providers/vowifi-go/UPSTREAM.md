# Upstream source and MDD patch

`upstream/vowifi-go` is a complete tracked-source snapshot of
`github.com/boa-z/vowifi-go` at commit
`1e9c6e6adbfcd9667695149d5ecb0f71cd062f07` (pseudo-version
`v0.0.0-20260709161034-1e9c6e6adbfc`). On 2026-08-28, the repository HEAD
reported by `git ls-remote` was the same commit.

MDD keeps this source local because the reviewed upstream API hard-coded
`net.Dialer` inside its SIP flow. The local patch is deliberately limited to:

- optional standard `DialContext` and local-bind `DialContextLocal` seams on
  wire REGISTER, request and shared flow transports;
- propagation of that seam from `WireIMSRegistrar`;
- use of the same seam for DNS queries, including prepared P-CSCF candidates;
- TLS wrapping and handshake after a caller-provided raw TCP dial;
- an optional transport-owned Security-Agree installer that receives the
  actual connected endpoints before the flow switches to the protected ports;
- null encryption and HMAC-MD5-96 support in the upstream ESP codec, alongside
  its existing AES-CBC and HMAC-SHA1-96 support;
- the proven MMTel INVITE feature tags on `Supported` and `Contact`, without
  changing dialog ownership or recovery behavior.

When these seams are nil, the original host-network and Security-Agree behavior
is unchanged.
MDD's `internal/ims` wrapper always supplies the SWu in-memory stack and rejects
custom transports, resolvers, local binding and security-plan installers whose
network provenance cannot be proved. This is fail-closed; it does not fall back
to the host network.

The upstream source remains AGPL-3.0 and retains its original license and
notices. Do not replace this snapshot without reviewing and replaying the small
userspace-dial patch against the new exact commit.
