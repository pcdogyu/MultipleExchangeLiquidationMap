package liqmap

import (
	"net"
	"net/url"
	"strings"
)

func capturePageURL(path string) string {
	base := strings.TrimSpace(getenv("CAPTURE_BASE_URL", ""))
	if base == "" {
		base = strings.TrimSpace(getenv("PUBLIC_BASE_URL", ""))
	}
	if base == "" {
		base = "http://" + captureHostPortFromAddr(getenv("APP_ADDR", getenv("APP_PORT", defaultServerAddr)))
	} else if !strings.Contains(base, "://") {
		base = "http://" + base
	}
	base = strings.TrimRight(base, "/")
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return base + path
}

func captureHostPortFromAddr(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		addr = defaultServerAddr
	}
	if !strings.Contains(addr, ":") {
		addr = ":" + strings.TrimPrefix(addr, ":")
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		if strings.HasPrefix(addr, ":") {
			host = ""
			port = strings.TrimPrefix(addr, ":")
		} else {
			host = addr
		}
	}
	host = strings.TrimSpace(host)
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		host = "127.0.0.1"
	}
	if port == "" {
		if parsed, err := url.Parse("http://" + host); err == nil {
			return parsed.Host
		}
		return host
	}
	return net.JoinHostPort(host, port)
}
