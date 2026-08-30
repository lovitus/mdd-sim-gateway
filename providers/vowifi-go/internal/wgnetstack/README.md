# WireGuard netstack compatibility shim

`tun.go` is derived from
`golang.zx2c4.com/wireguard/tun/netstack` at
`v0.0.0-20260522210424-ecfc5a8d5446` under its MIT license.

MDD adds only `DialContextTCPAddrPortWithBind`. It follows gVisor's
`gonet.DialTCPWithBind`, with `SO_REUSEADDR` set before binding so IMS can keep
its source port while failing over between distinct P-CSCF targets. IMS
Security-Agree also needs the authenticated TCP connection to use the
negotiated `port-c`; upstream WireGuard's public wrapper currently exposes
local binding only for UDP.

Delete this shim and return `internal/usernet` to the upstream package when an
equivalent method is released upstream.
