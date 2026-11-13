package server

import "testing"

func TestIsLoopbackHost(t *testing.T) {
	for host, want := range map[string]bool{
		"127.0.0.1": true, "localhost": true, "LOCALHOST": true, "::1": true, "[::1]": true, "127.0.0.2": true,
		"": false, "0.0.0.0": false, "::": false, "192.168.1.5": false, "example.com": false,
	} {
		if got := IsLoopbackHost(host); got != want {
			t.Errorf("IsLoopbackHost(%q)=%v, want %v", host, got, want)
		}
	}
}

func TestHostPolicy(t *testing.T) {
	cases := []struct {
		name   string
		bind   string
		header string
		want   bool
	}{
		{"loopback bind localhost", "127.0.0.1", "localhost:8080", true},
		{"loopback bind 127", "127.0.0.1", "127.0.0.1:8080", true},
		{"loopback bind ipv6", "127.0.0.1", "[::1]:8080", true},
		{"bare ipv6", "127.0.0.1", "::1", true},
		{"uppercase", "127.0.0.1", "LocalHost:1", true},
		{"no port", "127.0.0.1", "localhost", true},
		{"trailing dot", "127.0.0.1", "localhost.:1", true},
		{"rebinding name", "127.0.0.1", "evil.example:8080", false},
		{"rebinding name no port", "127.0.0.1", "evil.example", false},
		{"lan ip on loopback bind", "127.0.0.1", "192.168.1.5:8080", false},
		{"empty", "127.0.0.1", "", false},
		{"suffix trick", "127.0.0.1", "localhost.evil.example", false},
		{"explicit host", "192.168.1.5", "192.168.1.5:8080", true},
		{"explicit host still allows localhost", "192.168.1.5", "localhost:8080", true},
		{"explicit host other", "192.168.1.5", "192.168.1.6:8080", false},
		{"explicit dns host", "Box.Local", "box.local:8080", true},
		{"explicit dns host other name", "box.local", "evil.example", false},
		{"wildcard ip literal", "0.0.0.0", "192.168.1.5:8080", true},
		{"wildcard ipv6 literal", "::", "[fd00::1]:8080", true},
		{"wildcard dns name", "0.0.0.0", "evil.example:8080", false},
		{"wildcard localhost", "0.0.0.0", "localhost:8080", true},
		{"empty bind is wildcard", "", "10.0.0.2", true},
		{"empty bind dns", "", "evil.example", false},
	}
	for _, c := range cases {
		if got := newHostPolicy(c.bind).allows(c.header); got != c.want {
			t.Errorf("%s: bind %q Host %q = %v, want %v", c.name, c.bind, c.header, got, c.want)
		}
	}
}

func TestOriginIsOwn(t *testing.T) {
	cases := []struct {
		origin string
		port   int
		want   bool
	}{
		{"", 8080, true},
		{"http://localhost:8080", 8080, true},
		{"http://127.0.0.1:8080", 8080, true},
		{"http://[::1]:8080", 8080, true},
		{"http://LOCALHOST:8080", 8080, true},
		{"http://localhost:8081", 8080, false},
		{"http://localhost", 8080, false},
		{"https://localhost:8080", 8080, false},
		{"http://evil.example:8080", 8080, false},
		{"http://localhost", 80, true},
		{"http://localhost:80", 80, false}, // browsers never send the default port
		{"http://[::1]", 80, true},
		{"http://localhost:8080", 80, false},
	}
	for _, c := range cases {
		if got := originIsOwn(c.origin, c.port); got != c.want {
			t.Errorf("originIsOwn(%q, %d)=%v, want %v", c.origin, c.port, got, c.want)
		}
	}
}
