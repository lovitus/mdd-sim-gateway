# mdd-cellular-io third-party lock

The release build consumes source archives in CI and never compiles on the customer Mac.

- libusb `1.0.30`, LGPL-2.1-or-later; archive SHA-256
  `fea36f34f9156400209595e300840767ab1a385ede1dc7ee893015aea9c6dbaf`
- lwIP `STABLE-2_2_1_RELEASE`, BSD-3-Clause; tag archive SHA-256
  `ce0b7461c0ad9602c376f0bf07c5eb7253b48c7bf66f011c6bf3e2a96731c539`

The companion contains no MDD business state, WebSocket client, SOCKS listener, host DNS call,
or host Internet socket. It owns one raw USB modem attachment and exposes only the inherited
binary dial channel documented by `protocol.h`.
