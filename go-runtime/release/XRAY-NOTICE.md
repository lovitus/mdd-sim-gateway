# Xray-core

Upstream: https://github.com/XTLS/Xray-core
Release: v26.3.27
Source revision: d2758a023cd7f4174a5a5fa4ff66e487d4342ba0

Xray-core is distributed as an independent Go executable under its upstream
Mozilla Public License 2.0. MDD does not modify its source. The exact complete
source, including LICENSE, is provided in xray-source.tar.gz. Dependency license
texts are included under xray in go-dependency-licenses.tar.gz.
The GPL-3.0-or-later sing v0.5.1 and sing-shadowsocks v0.2.7 dependencies have
short upstream notices that the automated scanner cannot classify. Their exact
notices, complete module source archives, version index and full GPL v3 text are
included under xray-curated in that archive; this is not a license exemption.

The executable, source archive and this notice are a required group whenever
the Xray capability is included in a release. Their sizes and SHA-256 hashes
are recorded in manifest.json. The existing installer owns its stable symlink;
the application does not download dependencies at runtime.
