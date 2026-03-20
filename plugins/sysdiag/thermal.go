package sysdiag

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/cprobe/catpaw/digcore/diagnose"
)

const thermalBasePath = "/sys/class/thermal"

func registerThermal(registry *diagnose.ToolRegistry) {
	registry.RegisterCategory("sysdiag_thermal", "sysdiag:thermal",
		"Thermal diagnostic tools (CPU/device temperature). Linux only.",
		diagnose.ToolScopeLocal)

	registry.Register("sysdiag_thermal", diagnose.DiagnoseTool{
		Name:        "thermal_zone",
		Description: "Show thermal zone temperatures. Reads /sys/class/thermal/thermal_zone*/temp. Highlights zones above 80°C.",
		Scope:       diagnose.ToolScopeLocal,
		Execute:     execThermalZone,
	})
}

type thermalEntry struct {
	zone    string
	zoneType string
	tempC   float64
	trip    []tripPoint
}

type tripPoint struct {
	pointType string
	tempC     float64
}

func execThermalZone(_ context.Context, _ map[string]string) (string, error) {
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("thermal_zone requires linux (current: %s)", runtime.GOOS)
	}

	zones, err := readThermalZones(thermalBasePath)
	if err != nil {
		return "", err
	}

	return formatThermalZones(zones), nil
}

func readThermalZones(basePath string) ([]thermalEntry, error) {
	pattern := filepath.Join(basePath, "thermal_zone*")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("glob %s: %w", pattern, err)
	}
	if len(matches) == 0 {
		return nil, nil
	}

	sort.Strings(matches)
	var zones []thermalEntry

	for _, zonePath := range matches {
		zone := filepath.Base(zonePath)

		tempRaw, err := readTrimmedFile(filepath.Join(zonePath, "temp"))
		if err != nil {
			continue
		}
		tempMilli, err := strconv.ParseInt(tempRaw, 10, 64)
		if err != nil {
			continue
		}

		zoneType, _ := readTrimmedFile(filepath.Join(zonePath, "type"))

		entry := thermalEntry{
			zone:     zone,
			zoneType: zoneType,
			tempC:    float64(tempMilli) / 1000.0,
		}

		entry.trip = readTripPoints(zonePath)
		zones = append(zones, entry)
	}

	return zones, nil
}

func readTripPoints(zonePath string) []tripPoint {
	var trips []tripPoint
	for i := 0; i < 20; i++ {
		tempRaw, err := readTrimmedFile(filepath.Join(zonePath, fmt.Sprintf("trip_point_%d_temp", i)))
		if err != nil {
			break
		}
		tempMilli, err := strconv.ParseInt(tempRaw, 10, 64)
		if err != nil {
			continue
		}
		ptType, _ := readTrimmedFile(filepath.Join(zonePath, fmt.Sprintf("trip_point_%d_type", i)))
		trips = append(trips, tripPoint{
			pointType: ptType,
			tempC:     float64(tempMilli) / 1000.0,
		})
	}
	return trips
}

func readTrimmedFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func formatThermalZones(zones []thermalEntry) string {
	if len(zones) == 0 {
		return "No thermal zone data available."
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Thermal Zones: %d\n\n", len(zones))
	fmt.Fprintf(&b, "%-20s  %-20s  %8s  %s\n", "ZONE", "TYPE", "TEMP", "TRIP POINTS")
	b.WriteString(strings.Repeat("-", 75))
	b.WriteByte('\n')

	for _, z := range zones {
		marker := ""
		if z.tempC >= 95 {
			marker = " [!!!]"
		} else if z.tempC >= 80 {
			marker = " [!]"
		}

		trips := formatTripPoints(z.trip)
		fmt.Fprintf(&b, "%-20s  %-20s  %6.1f°C%s  %s\n",
			z.zone, z.zoneType, z.tempC, marker, trips)
	}

	return b.String()
}

func formatTripPoints(trips []tripPoint) string {
	if len(trips) == 0 {
		return ""
	}
	parts := make([]string, 0, len(trips))
	for _, tp := range trips {
		if tp.pointType != "" {
			parts = append(parts, fmt.Sprintf("%s:%.0f°C", tp.pointType, tp.tempC))
		} else {
			parts = append(parts, fmt.Sprintf("%.0f°C", tp.tempC))
		}
	}
	return strings.Join(parts, ", ")
}
