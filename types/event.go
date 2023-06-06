package types

import (
	"sort"
	"strings"

	"github.com/toolkits/pkg/str"
)

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

func (e *Event) SetEventTime(t int64) *Event {
	e.EventTime = t
	return e
}

func (e *Event) SetTitleRule(rule string) *Event {
	e.TitleRule = rule
	return e
}

func (e *Event) SetDescription(desc string) *Event {
	e.Description = desc
	return e
}

func BuildEvent(status string, labelMaps ...map[string]string) *Event {
	event := &Event{
		EventStatus: status,
	}

	labels := make(map[string]string)
	for _, labelMap := range labelMaps {
		for k, v := range labelMap {
			labels[k] = v
		}
	}

	count := len(labels)
	keys := make([]string, 0, count)
	for k := range labels {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	var sb strings.Builder
	for _, k := range keys {
		sb.WriteString(k)
		sb.WriteString(":")
		sb.WriteString(labels[k])
		sb.WriteString(":")
	}

	event.AlertKey = str.MD5(sb.String())

	return event
}
