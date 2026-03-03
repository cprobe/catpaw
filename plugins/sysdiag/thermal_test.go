package sysdiag

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadThermalZones(t *testing.T) {
	base := t.TempDir()

	// Create two thermal zones
	z0 := filepath.Join(base, "thermal_zone0")
	os.MkdirAll(z0, 0755)
	os.WriteFile(filepath.Join(z0, "temp"), []byte("45000\n"), 0644)
	os.WriteFile(filepath.Join(z0, "type"), []byte("x86_pkg_temp\n"), 0644)
	os.WriteFile(filepath.Join(z0, "trip_point_0_temp"), []byte("85000\n"), 0644)
	os.WriteFile(filepath.Join(z0, "trip_point_0_type"), []byte("critical\n"), 0644)

	z1 := filepath.Join(base, "thermal_zone1")
	os.MkdirAll(z1, 0755)
	os.WriteFile(filepath.Join(z1, "temp"), []byte("92000\n"), 0644)
	os.WriteFile(filepath.Join(z1, "type"), []byte("acpitz\n"), 0644)

	zones, err := readThermalZones(base)
	if err != nil {
		t.Fatalf("readThermalZones: %v", err)
	}
	if len(zones) != 2 {
		t.Fatalf("expected 2 zones, got %d", len(zones))
	}

	if zones[0].tempC != 45.0 {
		t.Errorf("zone0 temp=%.1f, want 45.0", zones[0].tempC)
	}
	if zones[0].zoneType != "x86_pkg_temp" {
		t.Errorf("zone0 type=%q, want x86_pkg_temp", zones[0].zoneType)
	}
	if len(zones[0].trip) != 1 {
		t.Errorf("zone0 trip points=%d, want 1", len(zones[0].trip))
	}
	if zones[1].tempC != 92.0 {
		t.Errorf("zone1 temp=%.1f, want 92.0", zones[1].tempC)
	}
}

func TestFormatThermalZones(t *testing.T) {
	zones := []thermalEntry{
		{zone: "thermal_zone0", zoneType: "x86_pkg_temp", tempC: 45.0,
			trip: []tripPoint{{pointType: "critical", tempC: 85}}},
		{zone: "thermal_zone1", zoneType: "acpitz", tempC: 92.0},
	}

	out := formatThermalZones(zones)
	if !strings.Contains(out, "2") {
		t.Fatal("expected zone count in output")
	}
	if !strings.Contains(out, "45.0") {
		t.Fatal("expected temp 45.0 in output")
	}
	// 92°C should trigger [!]
	if !strings.Contains(out, "[!]") {
		t.Fatal("expected [!] marker for 92°C")
	}
	if !strings.Contains(out, "critical:85°C") {
		t.Fatal("expected trip point in output")
	}
}

func TestFormatTripPoints(t *testing.T) {
	trips := []tripPoint{
		{pointType: "passive", tempC: 75},
		{pointType: "critical", tempC: 95},
	}
	out := formatTripPoints(trips)
	if !strings.Contains(out, "passive:75°C") {
		t.Fatal("expected passive trip point")
	}
	if !strings.Contains(out, "critical:95°C") {
		t.Fatal("expected critical trip point")
	}
}
