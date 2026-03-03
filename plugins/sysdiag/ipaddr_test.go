package sysdiag

import (
	"strings"
	"testing"
)

func TestParseIPAddrText(t *testing.T) {
	text := `1: lo: <LOOPBACK,UP,LOWER_UP> mtu 65536 qdisc noqueue state UNKNOWN group default qlen 1000
    link/loopback 00:00:00:00:00:00 brd 00:00:00:00:00:00
    inet 127.0.0.1/8 scope host lo
       valid_lft forever preferred_lft forever
    inet6 ::1/128 scope host
       valid_lft forever preferred_lft forever
2: eth0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 qdisc mq state UP group default qlen 1000
    link/ether aa:bb:cc:dd:ee:ff brd ff:ff:ff:ff:ff:ff
    inet 10.0.0.5/24 brd 10.0.0.255 scope global eth0
       valid_lft forever preferred_lft forever
    inet6 fe80::a8bb:ccff:fedd:eeff/64 scope link
       valid_lft forever preferred_lft forever
3: docker0: <NO-CARRIER,BROADCAST,MULTICAST,UP> mtu 1500 qdisc noqueue state DOWN group default
    link/ether 02:42:12:34:56:78 brd ff:ff:ff:ff:ff:ff
    inet 172.17.0.1/16 brd 172.17.255.255 scope global docker0
       valid_lft forever preferred_lft forever
`

	ifaces := parseIPAddrText(text)
	if len(ifaces) != 3 {
		t.Fatalf("expected 3 interfaces, got %d", len(ifaces))
	}

	lo := ifaces[0]
	if lo.Name != "lo" {
		t.Errorf("first interface name=%q, want 'lo'", lo.Name)
	}
	if lo.MTU != 65536 {
		t.Errorf("lo MTU=%d, want 65536", lo.MTU)
	}

	eth0 := ifaces[1]
	if eth0.Name != "eth0" {
		t.Errorf("second interface name=%q, want 'eth0'", eth0.Name)
	}
	if eth0.State != "UP" {
		t.Errorf("eth0 state=%q, want UP", eth0.State)
	}
	if eth0.MAC != "aa:bb:cc:dd:ee:ff" {
		t.Errorf("eth0 MAC=%q, want aa:bb:cc:dd:ee:ff", eth0.MAC)
	}
	if len(eth0.Addrs) != 2 {
		t.Fatalf("eth0 addrs=%d, want 2", len(eth0.Addrs))
	}
	if eth0.Addrs[0].Address != "10.0.0.5/24" {
		t.Errorf("eth0 addr[0]=%q, want 10.0.0.5/24", eth0.Addrs[0].Address)
	}

	docker0 := ifaces[2]
	if docker0.State != "DOWN" {
		t.Errorf("docker0 state=%q, want DOWN", docker0.State)
	}
}

func TestParseInterfaceHeader(t *testing.T) {
	line := "2: eth0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 qdisc mq state UP group default qlen 1000"
	iface := parseInterfaceHeader(line)
	if iface.Name != "eth0" {
		t.Errorf("name=%q, want eth0", iface.Name)
	}
	if iface.MTU != 1500 {
		t.Errorf("MTU=%d, want 1500", iface.MTU)
	}
	if iface.State != "UP" {
		t.Errorf("state=%q, want UP", iface.State)
	}
}

func TestFormatIPAddr(t *testing.T) {
	ifaces := []ipInterface{
		{Name: "eth0", State: "UP", MTU: 1500, MAC: "aa:bb:cc:dd:ee:ff",
			Addrs: []ipAddress{{Family: "inet", Address: "10.0.0.5/24", Scope: "global"}}},
		{Name: "docker0", State: "DOWN", MTU: 1500},
	}

	out := formatIPAddr(ifaces)
	if !strings.Contains(out, "1 DOWN") {
		t.Fatal("expected DOWN count in header")
	}
	if !strings.Contains(out, "eth0: UP") {
		t.Fatal("expected eth0 UP")
	}
	if !strings.Contains(out, "[!]") {
		t.Fatal("expected [!] for DOWN interface")
	}
	if !strings.Contains(out, "10.0.0.5/24") {
		t.Fatal("expected IP address")
	}
}

func TestFormatIPAddrEmpty(t *testing.T) {
	out := formatIPAddr(nil)
	if !strings.Contains(out, "No network") {
		t.Fatal("expected 'No network' message")
	}
}
