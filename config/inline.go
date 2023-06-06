package config

type InternalConfig struct {
	// append labels to every event
	Labels map[string]string `toml:"labels"`

	// gather interval
	Interval Duration `toml:"interval"`

	// whether instance initialized
	initialized bool `toml:"-"`
}

func (ic *InternalConfig) GetLabels() map[string]string {
	if ic.Labels != nil {
		return ic.Labels
	}

	return map[string]string{}
}

func (ic *InternalConfig) GetInitialized() bool {
	return ic.initialized
}

func (ic *InternalConfig) SetInitialized() {
	ic.initialized = true
}

func (ic *InternalConfig) GetInterval() Duration {
	return ic.Interval
}

func (ic *InternalConfig) InitInternalConfig() error {
	// maybe compile some glob/regex pattern here
	return nil
}
