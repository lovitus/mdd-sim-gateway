# MDD Compatibility Patch

Source: github.com/damonto/euicc-go v1.1.2, copied from the verified Go module cache.
The upstream MIT LICENSE is retained. No dependency version upgrade is included.

lpa/download.go and v2/es10b.go change behavior: downloads accept an omitted
IMEI; supplied IMEIs must contain 15 decimal digits. An absent IMEI emits the
lpac-compatible default TAC 35290611 and omits the IMEI TLV entirely. It does not
generate a replacement IMEI. Explicit IMEIs retain the existing encoding.

This restores the optional IMEI input in MDD ec620942's Esim.jsx and matches the
TAC-only handling already used by MDD's discoveryAuthenticateServerRequest.
No profile enable, delete, notification or certificate validation behavior changes.

http/client.go additionally retains non-2xx HTTP status as a typed StatusError and
closes the response body on that path. It does not expose URLs or response text.
MDD maps existing RSP subject/reason codes and typed network/TLS failures into the
existing durable download job code; it does not infer certificate incompatibility
from a generic error or alter historical receipts.

v2/types.go preserves StatusCodeData as an error instead of flattening it to its
message. The root agentsim regression uses actual HTTP JSON decoding through
InvokeHTTP and the production classifier, not only constructed error values.

apdu/transmitter.go retains unexpected status words as a typed error, without
including APDU contents. MDD's PC/SC profile operations use refresh=false and
unpower the owned card on session close after a successful enable/disable, matching
the original lpac CLI default and PC/SC disconnect behavior. Ordinary reads and
nickname changes keep their existing card-close disposition.
