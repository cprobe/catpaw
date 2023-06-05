package types

const (
	EventStatusCritical = "Critical"
	EventStatusWarning  = "Warning"
	EventStatusInfo     = "Info"
	EventStatusOk       = "Ok"
)

type Event struct {
	EventTime   int64             `json:"event_time"`
	EventStatus string            `json:"event_status"`
	AlertKey    string            `json:"alert_key"`
	Labels      map[string]string `json:"labels"`
	TitleRule   string            `json:"title_rule"` // $a::b::$c
	Description string            `json:"description"`
}
