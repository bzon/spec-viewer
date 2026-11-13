package server

import (
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// IsLoopbackHost reports whether a --host value binds only to the loopback
// interface: "localhost" or a loopback IP literal. The empty string is not
// loopback, because listening on ":port" binds every interface.
func IsLoopbackHost(host string) bool {
	host = normalizeHostname(host)
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// normalizeHostname lower-cases a bare host, removes IPv6 brackets and a
// trailing dot.
func normalizeHostname(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	return strings.TrimSuffix(host, ".")
}

// hostnameOf extracts the hostname from a Host header value: port stripped,
// IPv6 brackets removed, lower-cased.
func hostnameOf(hostHeader string) string {
	h := hostHeader
	if host, _, err := net.SplitHostPort(hostHeader); err == nil {
		h = host
	}
	return normalizeHostname(h)
}

// hostPolicy decides which Host headers a server answers. DNS rebinding
// needs the browser to send a Host naming an attacker-controlled DNS name, so
// the policy admits only names that cannot be one:
//   - localhost and loopback IP literals, always;
//   - for a server bound to an explicit non-loopback host, that host;
//   - for a wildcard bind (0.0.0.0, ::, or an empty host), any IP literal,
//     since the machine's addresses are not known up front and an IP literal
//     is not a DNS name an attacker can rebind. DNS names are refused.
type hostPolicy struct {
	configured string // normalized --host
	wildcard   bool
}

func newHostPolicy(host string) hostPolicy {
	h := normalizeHostname(host)
	ip := net.ParseIP(h)
	return hostPolicy{configured: h, wildcard: h == "" || (ip != nil && ip.IsUnspecified())}
}

func (p hostPolicy) allows(hostHeader string) bool {
	h := hostnameOf(hostHeader)
	if h == "" {
		return false
	}
	if IsLoopbackHost(h) {
		return true
	}
	if p.wildcard {
		return net.ParseIP(h) != nil
	}
	return h == p.configured
}

// middleware answers 421 Misdirected Request for a Host header the policy
// refuses, before any handler runs.
func (p hostPolicy) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !p.allows(r.Host) {
			http.Error(w, "misdirected request: unrecognised Host", http.StatusMisdirectedRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// loopbackOriginHosts lists the Origin host[:port] strings a page served by
// this server on the given port has: localhost, 127.0.0.1 and [::1], with the
// port omitted when it is http's default 80 (browsers drop it).
func loopbackOriginHosts(port int) []string {
	suffix := ""
	if port != 80 {
		suffix = ":" + strconv.Itoa(port)
	}
	return []string{"localhost" + suffix, "127.0.0.1" + suffix, "[::1]" + suffix}
}

// originIsOwn reports whether origin is empty or an http origin on a loopback
// host and exactly the given port.
func originIsOwn(origin string, port int) bool {
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "http" {
		return false
	}
	host := strings.ToLower(u.Host)
	for _, h := range loopbackOriginHosts(port) {
		if host == h {
			return true
		}
	}
	return false
}

// originPatterns is loopbackOriginHosts escaped for the path.Match style
// patterns the WebSocket library compares Origin hosts against.
func originPatterns(port int) []string {
	hosts := loopbackOriginHosts(port)
	for i, h := range hosts {
		hosts[i] = strings.NewReplacer(`[`, `\[`, `]`, `\]`).Replace(h)
	}
	return hosts
}
