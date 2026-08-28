module github.com/lovitus/mdd-sim-gateway/providers/vowifi-go

go 1.26.3

require (
	github.com/boa-z/vowifi-go v0.0.0-20260709161034-1e9c6e6adbfc
	github.com/coder/websocket v1.8.15
	github.com/lovitus/mdd-sim-gateway/go-runtime v0.0.0
	github.com/pion/rtcp v1.2.16
	github.com/pion/rtp v1.10.2
	github.com/zaf/g711 v1.4.0
	go.etcd.io/bbolt v1.5.0
	golang.org/x/net v0.57.0
	golang.zx2c4.com/wireguard v0.0.0-20260522210424-ecfc5a8d5446
)

replace github.com/boa-z/vowifi-go => ./upstream/vowifi-go

replace github.com/lovitus/mdd-sim-gateway/go-runtime => ../../go-runtime

require (
	github.com/emiago/sipgo v1.4.0 // indirect
	github.com/gobwas/httphead v0.1.0 // indirect
	github.com/gobwas/pool v0.2.1 // indirect
	github.com/gobwas/ws v1.4.0 // indirect
	github.com/google/btree v1.1.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/pion/logging v0.2.4 // indirect
	github.com/pion/randutil v0.1.0 // indirect
	github.com/pion/srtp/v3 v3.0.12 // indirect
	github.com/pion/transport/v4 v4.0.2 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/time v0.14.0 // indirect
	golang.zx2c4.com/wintun v0.0.0-20230126152724-0fa3db229ce2 // indirect
	gvisor.dev/gvisor v0.0.0-20250503011706-39ed1f5ac29c // indirect
)
