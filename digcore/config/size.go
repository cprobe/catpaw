package config

import (
	"fmt"
	"strconv"
	"strings"
)

// Size represents a byte size with human-readable TOML parsing.
// Supports: "50MB", "1GB", "512KB", "1024" (bytes), etc.
// Case-insensitive units: B, KB, MB, GB, TB.
type Size int64

const (
	_          = iota
	KB Size = 1 << (10 * iota)
	MB
	GB
	TB
)

func (s *Size) UnmarshalTOML(b []byte) error {
	str := strings.ReplaceAll(string(b), "'", "")
	str = strings.ReplaceAll(str, "\"", "")
	str = strings.TrimSpace(str)

	if str == "" || str == "0" {
		*s = 0
		return nil
	}

	// Pure integer → treat as bytes
	if n, err := strconv.ParseInt(str, 10, 64); err == nil {
		*s = Size(n)
		return nil
	}

	upper := strings.ToUpper(str)
	units := []struct {
		suffix string
		mult   Size
	}{
		{"TB", TB},
		{"GB", GB},
		{"MB", MB},
		{"KB", KB},
		{"B", 1},
	}

	for _, u := range units {
		if strings.HasSuffix(upper, u.suffix) {
			numStr := strings.TrimSpace(str[:len(str)-len(u.suffix)])
			n, err := strconv.ParseFloat(numStr, 64)
			if err != nil {
				return fmt.Errorf("invalid size value: %q", str)
			}
			*s = Size(n * float64(u.mult))
			return nil
		}
	}

	return fmt.Errorf("invalid size format: %q (use B, KB, MB, GB, TB)", str)
}

func (s *Size) UnmarshalText(text []byte) error {
	return s.UnmarshalTOML(text)
}

func (s Size) String() string {
	switch {
	case s >= TB:
		return fmt.Sprintf("%.1fTB", float64(s)/float64(TB))
	case s >= GB:
		return fmt.Sprintf("%.1fGB", float64(s)/float64(GB))
	case s >= MB:
		return fmt.Sprintf("%.1fMB", float64(s)/float64(MB))
	case s >= KB:
		return fmt.Sprintf("%.1fKB", float64(s)/float64(KB))
	default:
		return fmt.Sprintf("%dB", s)
	}
}
