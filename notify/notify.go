package notify

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cprobe/catpaw/config"
	"github.com/cprobe/catpaw/logger"
	"github.com/cprobe/catpaw/types"
)

type Notifier interface {
	Name() string
	Forward(event *types.Event) bool
}

var notifiers []Notifier

func Register(n Notifier) {
	notifiers = append(notifiers, n)
	logger.Logger.Infow("notifier registered", "name", n.Name())
}

func Forward(event *types.Event) bool {
	if config.Config.TestMode {
		PrintStdout(event)
		return true
	}

	if len(notifiers) == 0 {
		logger.Logger.Warnw("forward: no notifiers configured, event dropped",
			"event_key", event.AlertKey)
		return false
	}

	anyOk := false
	for _, n := range notifiers {
		if n.Forward(event) {
			anyOk = true
		}
	}
	return anyOk
}

func PrintStdout(event *types.Event) {
	var sb strings.Builder

	sb.WriteString(fmt.Sprint(event.EventTime))
	sb.WriteString(" ")
	sb.WriteString(time.Unix(event.EventTime, 0).Format("15:04:05"))
	sb.WriteString(" ")
	sb.WriteString(event.AlertKey)
	sb.WriteString(" ")
	sb.WriteString(event.EventStatus)
	sb.WriteString(" ")

	keys := make([]string, 0, len(event.Labels))
	for k := range event.Labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for i, k := range keys {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(k)
		sb.WriteByte('=')
		sb.WriteString(event.Labels[k])
	}

	if len(event.Attrs) > 0 {
		sb.WriteString(" attrs={")
		attrKeys := make([]string, 0, len(event.Attrs))
		for k := range event.Attrs {
			attrKeys = append(attrKeys, k)
		}
		sort.Strings(attrKeys)
		for i, k := range attrKeys {
			if i > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString(k)
			sb.WriteByte('=')
			sb.WriteString(event.Attrs[k])
		}
		sb.WriteByte('}')
	}

	sb.WriteString(" ")
	sb.WriteString(event.Description)

	fmt.Println(sb.String())
}
