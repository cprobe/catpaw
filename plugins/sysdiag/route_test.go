package sysdiag

import (
	"strings"
	"testing"
)

func TestParseRouteLine(t *testing.T) {
	tests := []struct {
		line    string
		dst     string
		gw      string
		dev     string
		metric  string
		isDeflt bool
	}{
		{
			"default via 10.0.0.1 dev eth0 proto dhcp metric 100",
			"default", "10.0.0.1", "eth0", "100", true,
		},
		{
			"10.0.0.0/24 dev eth0 proto kernel scope link src 10.0.0.5",
			"10.0.0.0/24", "", "eth0", "", false,
		},
		{
			"172.17.0.0/16 dev docker0 proto kernel scope link src 172.17.0.1 linkdown",
			"172.17.0.0/16", "", "docker0", "", false,
		},
	}
	for _, tt := range tests {
		r := parseRouteLine(tt.line)
		if r.dst != tt.dst {
			t.Errorf("line %q: dst=%q, want %q", tt.line, r.dst, tt.dst)
		}
		if r.gateway != tt.gw {
			t.Errorf("line %q: gateway=%q, want %q", tt.line, r.gateway, tt.gw)
		}
		if r.dev != tt.dev {
			t.Errorf("line %q: dev=%q, want %q", tt.line, r.dev, tt.dev)
		}
		if r.metric != tt.metric {
			t.Errorf("line %q: metric=%q, want %q", tt.line, r.metric, tt.metric)
		}
		if r.isDefault != tt.isDeflt {
			t.Errorf("line %q: isDefault=%v, want %v", tt.line, r.isDefault, tt.isDeflt)
		}
	}
}

func TestParseRouteText(t *testing.T) {
	text := `default via 10.0.0.1 dev eth0 proto dhcp metric 100
10.0.0.0/24 dev eth0 proto kernel scope link src 10.0.0.5
172.17.0.0/16 dev docker0 proto kernel scope link src 172.17.0.1 linkdown
`
	routes := parseRouteText(text)
	if len(routes) != 3 {
		t.Fatalf("expected 3 routes, got %d", len(routes))
	}
	if !routes[0].isDefault {
		t.Fatal("first route should be default")
	}
}

func TestFormatRoutes(t *testing.T) {
	routes := []routeEntry{
		{dst: "default", gateway: "10.0.0.1", dev: "eth0", protocol: "dhcp", metric: "100", isDefault: true},
		{dst: "10.0.0.0/24", dev: "eth0", protocol: "kernel", scope: "link"},
	}

	out := formatRoutes(routes, false)
	if !strings.Contains(out, "2 routes") {
		t.Fatal("expected route count")
	}
	if !strings.Contains(out, "10.0.0.1") {
		t.Fatal("expected gateway in output")
	}
	if !strings.Contains(out, "*") {
		t.Fatal("expected * marker for default route")
	}
}

func TestFormatRoutesNoDefault(t *testing.T) {
	routes := []routeEntry{
		{dst: "10.0.0.0/24", dev: "eth0"},
	}
	out := formatRoutes(routes, false)
	if !strings.Contains(out, "NO DEFAULT ROUTE") {
		t.Fatal("expected NO DEFAULT ROUTE warning")
	}
}
