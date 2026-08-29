# Embedded QR decoder

`decode.js`, `index.js`, and `LICENSE` come from the
`qr@0.6.0` npm package (`paulmillr/qr`, release tag `0.6.0`). The package is
dual-licensed MIT or Apache-2.0; MDD uses it under Apache-2.0. Two upstream
trailing-whitespace occurrences were normalized to satisfy repository checks;
no executable token or license text was changed.

- npm integrity: `sha512-P23VoX7SipHALdiIYG+D+LT/6n22dNKwV92FAb3d+Nlki/5WisSsfLt0UDFz2XEBtuwrECTznvu+chKKFCSYhA==`
- npm shasum: `00c3d080dc76adf5d3754d9ad7ff0f9263dee2e0`
- upstream: <https://github.com/paulmillr/qr>

Only the browser-side eSIM QR image reader imports `decode.js`. No image or
decoded activation code is sent to Core until the user separately confirms a
normal profile download.
