package main

import "testing"

func TestIsLoopbackListen(t *testing.T) {
	cases := []struct {
		listen string
		want   bool
	}{
		{"127.0.0.1:8090", true},
		{"127.0.0.1:0", true},
		{"[::1]:8443", true},
		{"localhost:8443", true}, // resolves to loopback
		{":8443", false},         // all interfaces
		{"0.0.0.0:8443", false},
		{"192.168.1.10:8443", false},
		{"[::]:8443", false},
		{"no-such-port", false}, // unparseable → treat as exposed
	}
	for _, tc := range cases {
		if got := isLoopbackListen(tc.listen); got != tc.want {
			t.Errorf("isLoopbackListen(%q) = %v, want %v", tc.listen, got, tc.want)
		}
	}
}
