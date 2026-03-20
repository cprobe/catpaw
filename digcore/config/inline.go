package config

type Alerting struct {
	Disabled bool `toml:"disabled"`

	// like prometheus `for`
	ForDuration Duration `toml:"for_duration"`

	// repeat interval
	RepeatInterval Duration `toml:"repeat_interval"`

	// maximum number of notifications
	RepeatNumber int `toml:"repeat_number"`

	// set to true to suppress recovery notifications
	DisableRecoveryNotification bool `toml:"disable_recovery_notification"`
}

type DiagnoseConfig struct {
	Enabled     bool     `toml:"enabled"`
	MinSeverity string   `toml:"min_severity"`
	Timeout     Duration `toml:"timeout"`
	Cooldown    Duration `toml:"cooldown"`
}

type InternalConfig struct {
	// append labels to every event
	Labels map[string]string `toml:"labels"`

	// gather interval
	Interval Duration `toml:"interval"`

	// alerting rule
	Alerting Alerting `toml:"alerting"`

	// AI diagnose config
	Diagnose DiagnoseConfig `toml:"diagnose"`
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

func (ic *InternalConfig) GetDiagnoseConfig() DiagnoseConfig {
	return ic.Diagnose
}
