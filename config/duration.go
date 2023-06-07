package config

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Duration is a time.Duration
type Duration time.Duration

// UnmarshalTOML parses the duration from the TOML config file
func (d *Duration) UnmarshalTOML(b []byte) error {
	// convert to string
	durStr := string(b)

	// Value is a TOML number (e.g. 3, 10, 3.5)
	// First try parsing as integer seconds
	sI, err := strconv.ParseInt(durStr, 10, 64)
	if err == nil {
		dur := time.Second * time.Duration(sI)
		*d = Duration(dur)
		return nil
	}
	// Second try parsing as float seconds
	sF, err := strconv.ParseFloat(durStr, 64)
	if err == nil {
		dur := time.Second * time.Duration(sF)
		*d = Duration(dur)
		return nil
	}

	// Finally, try value is a TOML string (e.g. "3s", 3s) or literal (e.g. '3s')
	durStr = strings.ReplaceAll(durStr, "'", "")
	durStr = strings.ReplaceAll(durStr, "\"", "")
	if durStr == "" {
		durStr = "0s"
	}

	dur, err := time.ParseDuration(durStr)
	if err != nil {
		return err
	}

	*d = Duration(dur)
	return nil
}

func (d *Duration) UnmarshalText(text []byte) error {
	return d.UnmarshalTOML(text)
}

func (d *Duration) HumanString() string {
	duration := time.Duration(*d)
	if duration.Seconds() < 60.0 {
		return fmt.Sprintf("%d seconds", int64(duration.Seconds()))
	}
	if duration.Minutes() < 60.0 {
		remainingSeconds := math.Mod(duration.Seconds(), 60)
		return fmt.Sprintf("%d minutes %d seconds", int64(duration.Minutes()), int64(remainingSeconds))
	}
	if duration.Hours() < 24.0 {
		remainingMinutes := math.Mod(duration.Minutes(), 60)
		remainingSeconds := math.Mod(duration.Seconds(), 60)
		return fmt.Sprintf("%d hours %d minutes %d seconds",
			int64(duration.Hours()), int64(remainingMinutes), int64(remainingSeconds))
	}
	remainingHours := math.Mod(duration.Hours(), 24)
	remainingMinutes := math.Mod(duration.Minutes(), 60)
	remainingSeconds := math.Mod(duration.Seconds(), 60)
	return fmt.Sprintf("%d days %d hours %d minutes %d seconds",
		int64(duration.Hours()/24), int64(remainingHours),
		int64(remainingMinutes), int64(remainingSeconds))
}
