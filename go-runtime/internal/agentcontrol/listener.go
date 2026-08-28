package agentcontrol

import (
	"errors"
	"net"
	"strconv"
	"strings"
)

func ListenLoopback(address string) (net.Listener, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port == "" || port == "0" {
		return nil, errors.New("Agent control address must use a fixed loopback port")
	}
	if _, err := strconv.ParseUint(port, 10, 16); err != nil {
		return nil, errors.New("Agent control address has an invalid port")
	}
	if strings.EqualFold(host, "localhost") {
		return nil, errors.New("Agent control singleton requires a literal loopback address")
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return nil, errors.New("Agent control address must be loopback")
	}
	return net.Listen("tcp", address)
}
