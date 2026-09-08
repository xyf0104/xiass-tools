// Package proxyendpoint owns the fixed-width loopback endpoint contract shared
// by the proxy and the Antigravity patcher.  Language Server binaries are
// patched in-place, so a replacement address must retain the original
// five-digit TCP port width.
package proxyendpoint

import (
	"fmt"
	"strconv"
)

const (
	// DefaultPort is retained for existing installations which were patched by
	// earlier releases before a runtime port state file existed.
	DefaultPort = 50999

	// MinPort and MaxPort deliberately restrict the helper to five-digit TCP
	// ports.  That makes every endpoint below exactly the same byte length as
	// the historical 50999 endpoint when it is embedded in a binary.
	MinPort = 10000
	MaxPort = 65535
)

// Endpoint is the complete set of local URLs inserted into supported
// Antigravity resources. Only the loopback port is runtime-selectable.
type Endpoint struct {
	Port          int
	Base          string
	Text          string
	Binary        string
	BinarySandbox string
}

func IsSupportedPort(port int) bool {
	return port >= MinPort && port <= MaxPort
}

func ForPort(port int) (Endpoint, error) {
	if !IsSupportedPort(port) {
		return Endpoint{}, fmt.Errorf("本地代理端口必须是 %d-%d 之间的五位端口", MinPort, MaxPort)
	}
	base := "http://127.0.0.1:" + strconv.Itoa(port)
	return Endpoint{
		Port:          port,
		Base:          base,
		Text:          base + "/v1internal/antigravity-wf",
		Binary:        base + "/v1internal/wfproxy",
		BinarySandbox: base + "/v1internal/wfproxy-sandbox",
	}, nil
}

func MustForPort(port int) Endpoint {
	endpoint, err := ForPort(port)
	if err != nil {
		panic(err)
	}
	return endpoint
}
