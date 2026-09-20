# Embedded data and retired runtime provenance

The September 20 audit removed unused Asterisk templates/C patches and the retired Control resource directory.
The deletion inventory records each original path, length and SHA-256 in
[retired-resources-3e6d5db.json](reviews/retired-resources-3e6d5db.json). Original contents remain in the pinned
Git revision named there. Removal changes the source distribution surface, not the license of historical copies.

The **live** country lookup is `go-runtime/internal/linecatalog/mcc_country.json`, embedded by country.go.
Its historical source is `ec620942` Control country data and lookup semantics. The live APN provider data is
`go-runtime/internal/agentpolicy/serviceproviders.xml`, embedded by apn_provider.go; its public-domain dedication
and author notice are retained inside the XML. Removing the old agent/resources copy does not remove APN support.
The manifest records both hashes and whether each duplicate was byte-identical; CI protects the retained copies.

The AOSP carrier-list textproto was used by retired Python Control but has no current Go consumer. Its original
Apache-2.0 notice and pinned upstream revision remain in the historical documents/Git revision. Its removal is
not a claim of complete MVNO matching parity; current country/APN behavior is defined by the embedded Go data/tests.

Legacy Python/Asterisk/VPCD package notices are preserved as historical attribution in docs/archive/2026-09-20.
Current release dependencies and required source/notice archives are described in THIRD_PARTY_LICENSES.md and the
Provider/native-helper notices. Neither Asterisk nor a proprietary codec module is restored as a runtime dependency.
