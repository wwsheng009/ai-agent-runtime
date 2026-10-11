package tunnel

import "testing"

func TestRouteAllowed(t *testing.T) {
	cases := []struct {
		routes []string
		path   string
		want   bool
	}{
		{[]string{"/"}, "/anything", true},
		{[]string{"/web", "/web/api"}, "/web/", true},
		{[]string{"/web", "/web/api"}, "/web/api/screen", true},
		{[]string{"/web"}, "/web", true},
		{[]string{"/web"}, "/web?x=1", true},
		{[]string{"/web"}, "/webx", false},
		{[]string{"/web"}, "/api/runtime", false},
		{[]string{"/api/runtime"}, "/api/runtime/sessions", true},
		{[]string{"/api/runtime"}, "/api/runtimex", false},
		{nil, "/web", false},
		{[]string{"  "}, "/web", false},
		{[]string{"/web"}, "/web/../secret", false},
	}
	for _, tc := range cases {
		if got := routeAllowed(tc.routes, tc.path); got != tc.want {
			t.Fatalf("routeAllowed(%v, %q) = %v, want %v", tc.routes, tc.path, got, tc.want)
		}
	}
}
