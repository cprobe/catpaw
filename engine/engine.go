package engine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"flashcat.cloud/catpaw/config"
	"flashcat.cloud/catpaw/logger"
	"flashcat.cloud/catpaw/pkg/safe"
	"flashcat.cloud/catpaw/plugins"
	"flashcat.cloud/catpaw/types"
)

func PushRawEvents(pluginName string, pluginObject plugins.Plugin, queue *safe.Queue[*types.Event]) {
	if queue.Len() == 0 {
		return
	}

	now := time.Now().Unix()
	events := queue.PopBackAll()

	for i := range events {
		if events[i] == nil {
			continue
		}

		err := clean(events[i], now, pluginName)
		if err != nil {
			logger.Logger.Error("clean raw event fail: "+err.Error(), "event", events[i])
			continue
		}

		logger.Logger.Debugf("event:%s: raw data received. status: %s, labels: %v", events[i].AlertKey, events[i].EventStatus, events[i].Labels)

		// engine logic
		if alertingCompute(pluginName, pluginObject, events[i]) {
			if events[i].EventStatus == types.EventStatusOk {
				// ok event
				Events.Del(events[i].AlertKey)
				if pluginObject.GetAlerting().RecoveryNotification {
					forward(events[i])
				}
				continue
			}

			repeatNumber := int64(pluginObject.GetAlerting().RepeatNumber)
			if repeatNumber > 0 && events[i].NotifyCount >= repeatNumber {
				// repeat number reached
				continue
			}

			repeatInterval := pluginObject.GetAlerting().RepeatInterval
			if events[i].LastSent > 0 && repeatInterval > 0 && now-events[i].LastSent < int64(repeatInterval/config.Duration(time.Second)) {
				// repeat interval not reached
				continue
			}

			events[i].LastSent = now
			events[i].NotifyCount++
			Events.Set(events[i])

			forward(events[i])
		}
	}
}

// forward if return true
// status changed? reach for_duration?
func alertingCompute(pluginName string, pluginObject plugins.Plugin, event *types.Event) bool {
	alertingRule := pluginObject.GetAlerting()

	if !alertingRule.Enabled {
		return false
	}

	old := Events.Get(event.AlertKey)

	if old == nil {
		event.FirstFireTime = event.EventTime

		// new event
		if event.EventStatus == types.EventStatusOk {
			return false
		}

		if alertingRule.ForDuration == 0 {
			// no need waiting
			return true
		}

		// waiting for duration
		Events.Set(event)
		return false
	}

	// compare old event
	if event.EventStatus == types.EventStatusOk {
		return true
	}

	if event.EventTime-old.FirstFireTime > int64(alertingRule.ForDuration/config.Duration(time.Second)) {
		return true
	}

	return false
}

func clean(event *types.Event, now int64, pluginName string) error {
	if event.EventTime == 0 {
		event.EventTime = now
	}

	if event.AlertKey == "" {
		return fmt.Errorf("alert key is blank")
	}

	if types.EventStatusValid(event.EventStatus) {
		return fmt.Errorf("invalid event_status: %s", event.EventStatus)
	}

	// append label: from_plugin
	if event.Labels != nil {
		event.Labels = make(map[string]string)
		event.Labels["from_plugin"] = pluginName
	}

	// append label: global labels
	var (
		hostname string
		err      error
	)

	if config.Config.Global.LabelHasHostname {
		hostname, err = os.Hostname()
		if err != nil {
			return fmt.Errorf("failed to get hostname: %s", err.Error())
		}
	}

	for key := range config.Config.Global.Labels {
		if strings.Contains(config.Config.Global.Labels[key], "$hostname") {
			config.Config.Global.Labels[key] = strings.ReplaceAll(config.Config.Global.Labels[key], "$hostname", hostname)
		}
	}

	return nil
}

func forward(event *types.Event) error {
	if config.Config.TestMode {
		printStdout(event)
		return nil
	}

	bs, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("event:%s: marshal fail: %s", event.AlertKey, err.Error())
	}

	req, err := http.NewRequest("POST", config.Config.Flashduty.Url, bytes.NewReader(bs))
	if err != nil {
		return fmt.Errorf("event:%s: new request fail: %s", event.AlertKey, err.Error())
	}

	res, err := config.Config.Flashduty.Client.Do(req)
	if err != nil {
		return fmt.Errorf("event:%s: do request fail: %s", event.AlertKey, err.Error())
	}

	var body []byte
	if res.Body != nil {
		defer res.Body.Close()
		body, err = io.ReadAll(res.Body)
		if err != nil {
			return fmt.Errorf("event:%s: read response fail: %s", event.AlertKey, err.Error())
		}
	}

	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("event:%s: request fail: %s, response: %s", event.AlertKey, res.Status, string(body))
	}

	logger.Logger.Debugf("event:%s: forwarded", event.AlertKey)

	return nil
}

func printStdout(event *types.Event) {
	var sb strings.Builder

	sb.WriteString(fmt.Sprint(event.EventTime))
	sb.WriteString(" ")
	sb.WriteString(time.Unix(event.EventTime, 0).Format("15:04:05"))
	sb.WriteString(" ")
	sb.WriteString(event.AlertKey)
	sb.WriteString(" ")
	sb.WriteString(event.EventStatus)
	sb.WriteString(" ")

	i := 0
	for k, v := range event.Labels {
		sb.WriteString(fmt.Sprintf("%s=%s", k, v))
		i++
		if i != len(event.Labels)-1 {
			sb.WriteString(",")
		}
	}

	sb.WriteString(" ")
	sb.WriteString(event.TitleRule)
	sb.WriteString(" ")
	sb.WriteString(event.TitleRule)
	sb.WriteString(" ")
	sb.WriteString(event.Description)

	fmt.Println(sb.String())
}
