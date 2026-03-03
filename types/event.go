package types

const (
	EventStatusCritical = "Critical"
	EventStatusWarning  = "Warning"
	EventStatusInfo     = "Info"
	EventStatusOk       = "Ok"

	DescFormatText     = ""
	DescFormatMarkdown = "markdown"

	// AttrCurrentValue is the standard Attrs key for the primary metric value
	// that triggered an alert. Plugins should set this so that diagnosis records
	// and AI prompts can display it consistently.
	AttrCurrentValue = "current_value"
)

type Event struct {
	EventTime   int64             `json:"event_time"`
	EventStatus string            `json:"event_status"`
	AlertKey    string            `json:"alert_key"`
	Labels      map[string]string `json:"labels"`
	Attrs             map[string]string `json:"attrs,omitempty"`
	Description       string            `json:"description"`
	DescriptionFormat string            `json:"description_format,omitempty"`

	// for internal use
	FirstFireTime int64 `json:"-"`
	NotifyCount   int64 `json:"-"`
	LastSent      int64 `json:"-"`
}

func EventStatusValid(status string) bool {
	switch status {
	case EventStatusCritical, EventStatusWarning, EventStatusInfo, EventStatusOk:
		return true
	default:
		return false
	}
}

func (e *Event) SetEventStatus(status string) *Event {
	e.EventStatus = status
	return e
}

func (e *Event) SetEventTime(t int64) *Event {
	e.EventTime = t
	return e
}

func (e *Event) SetAttrs(attrs map[string]string) *Event {
	if e.Attrs == nil {
		e.Attrs = make(map[string]string, len(attrs))
	}
	for k, v := range attrs {
		e.Attrs[k] = v
	}
	return e
}

func (e *Event) SetCurrentValue(v string) *Event {
	if e.Attrs == nil {
		e.Attrs = make(map[string]string, 1)
	}
	e.Attrs[AttrCurrentValue] = v
	return e
}

func (e *Event) SetDescription(desc string) *Event {
	e.Description = desc
	return e
}

// EvaluateGeThreshold returns the event status for a "greater-than-or-equal"
// threshold pair. A threshold value of 0 means "not configured / disabled".
func EvaluateGeThreshold(value, warnGe, criticalGe float64) string {
	if criticalGe > 0 && value >= criticalGe {
		return EventStatusCritical
	}
	if warnGe > 0 && value >= warnGe {
		return EventStatusWarning
	}
	return EventStatusOk
}

func BuildEvent(labelMaps ...map[string]string) *Event {
	event := &Event{
		EventStatus: EventStatusOk,
	}

	event.Labels = make(map[string]string)
	for _, labelMap := range labelMaps {
		for k, v := range labelMap {
			event.Labels[k] = v
		}
	}

	return event
}
