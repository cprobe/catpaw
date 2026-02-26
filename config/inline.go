package config

import "flashcat.cloud/catpaw/types"

type Alerting struct {
	Enabled bool `toml:"enabled"`

	// like prometheus `for`
	ForDuration Duration `toml:"for_duration"`

	// repeat interval
	RepeatInterval Duration `toml:"repeat_interval"`

	// maximum number of notifications
	RepeatNumber int `toml:"repeat_number"`

	// whether send recovery notification
	RecoveryNotification bool `toml:"recovery_notification"`

	// alert severity
	DefaultSeverity string `toml:"default_severity"`
}

type InternalConfig struct {
	// append labels to every event
	Labels map[string]string `toml:"labels"`

	// gather interval
	Interval Duration `toml:"interval"`

	// alerting rule
	Alerting Alerting `toml:"alerting"`
}

func (ic *InternalConfig) GetLabels() map[string]string {
	if ic.Labels != nil {
		return ic.Labels
	}

	return map[string]string{}
}

func (ic *InternalConfig) GetInterval() Duration {
	return ic.Interval
}

func (ic *InternalConfig) GetAlerting() Alerting {
	return ic.Alerting
}

func (ic *InternalConfig) GetDefaultSeverity() string {
	if ic.Alerting.DefaultSeverity == "" {
		return types.EventStatusWarning
	}

	return ic.Alerting.DefaultSeverity
}
