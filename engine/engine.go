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

func PushRawEvents(pluginName string, ins plugins.Instance, queue *safe.Queue[*types.Event]) {
	if queue.Len() == 0 {
		return
	}

	now := time.Now().Unix()
	events := queue.PopBackAll()

	for i := range events {
		if events[i] == nil {
			continue
		}

		err := clean(events[i], now, pluginName, ins)
		if err != nil {
			logger.Logger.Errorf("clean raw event fail: %v, event: %v", err.Error(), events[i])
			continue
		}

		logger.Logger.Debugf("event:%s: raw data received. event object: %v", events[i].AlertKey, events[i])

		if !ins.GetAlerting().Enabled {
			continue
		}

		if events[i].EventStatus == types.EventStatusOk {
			handleRecoveryEvent(ins, events[i])
		} else {
			handleAlertEvent(ins, events[i])
		}
	}
}

// 处理恢复事件
func handleRecoveryEvent(ins plugins.Instance, event *types.Event) {
	old := Events.Get(event.AlertKey)
	if old == nil {
		// 之前没有产生Event，当下的情况也是正常的，这是大多数场景，忽略即可，无需做任何处理
		return
	}

	// 之前产生了告警，现在恢复了，事件就可以从缓存删除了
	Events.Del(old.AlertKey)

	// 不过，也得看具体 alerting 的配置，如果不需要发送恢复通知，则忽略
	if ins.GetAlerting().RecoveryNotification {
		event.LastSent = event.EventTime
		event.FirstFireTime = old.FirstFireTime
		event.NotifyCount = old.NotifyCount + 1
		forward(event)
	}
}

// 处理告警事件
func handleAlertEvent(ins plugins.Instance, event *types.Event) {
	alerting := ins.GetAlerting()
	old := Events.Get(event.AlertKey)
	if old == nil {
		// 第一次产生告警事件
		event.FirstFireTime = event.EventTime

		// 无论如何，这个事件都得缓存起来
		Events.Set(event)

		// 要不要发？分两种情况。ForDuration 是 0 则立马发，否则等待 ForDuration 时间后再发
		if alerting.ForDuration == 0 {
			event.LastSent = event.EventTime
			event.NotifyCount++
			forward(event)
			return
		}

		return
	}

	// old != nil 这已经不是第一次产生告警事件了
	// 如果 ForDuration 没有满足，则不能继续发送
	if alerting.ForDuration > 0 && event.EventTime-old.FirstFireTime < int64(alerting.ForDuration/config.Duration(time.Second)) {
		return
	}

	// ForDuration 满足了，可以继续发送了
	// 首先看是否达到最大发送次数
	if alerting.RepeatNumber > 0 && old.NotifyCount >= int64(alerting.RepeatNumber) {
		return
	}

	// 其次看发送频率，不能发的太快了
	if alerting.RepeatInterval > 0 && event.EventTime-old.LastSent < int64(alerting.RepeatInterval/config.Duration(time.Second)) {
		return
	}

	// 最后，可以发了
	event.LastSent = event.EventTime
	event.NotifyCount = old.NotifyCount + 1
	Events.Set(event)
	forward(event)
}

func clean(event *types.Event, now int64, pluginName string, ins plugins.Instance) error {
	if event.EventTime == 0 {
		event.EventTime = now
	}

	if event.AlertKey == "" {
		return fmt.Errorf("alert key is blank")
	}

	if !types.EventStatusValid(event.EventStatus) {
		return fmt.Errorf("invalid event_status: %s", event.EventStatus)
	}

	if event.Labels == nil {
		event.Labels = make(map[string]string)
	}

	// append label: from_plugin
	event.Labels["from_plugin"] = pluginName

	// append label: from_instance
	insLabels := ins.GetLabels()
	for k, v := range insLabels {
		event.Labels[k] = v
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
			event.Labels[key] = strings.ReplaceAll(config.Config.Global.Labels[key], "$hostname", hostname)
		} else {
			event.Labels[key] = config.Config.Global.Labels[key]
		}
	}

	return nil
}

func forward(event *types.Event) {
	if config.Config.TestMode {
		printStdout(event)
		return
	}

	bs, err := json.Marshal(event)
	if err != nil {
		logger.Logger.Errorf("event:%s: forward: marshal fail: %s", event.AlertKey, err.Error())
	}

	req, err := http.NewRequest("POST", config.Config.Flashduty.Url, bytes.NewReader(bs))
	if err != nil {
		logger.Logger.Errorf("event:%s: forward: new request fail: %s", event.AlertKey, err.Error())
		return
	}

	req.Header.Set("Content-Type", "application/json")

	res, err := config.Config.Flashduty.Client.Do(req)
	if err != nil {
		logger.Logger.Errorf("event:%s: forward: do request fail: %s", event.AlertKey, err.Error())
		return
	}

	var body []byte
	if res.Body != nil {
		defer res.Body.Close()
		body, err = io.ReadAll(res.Body)
		if err != nil {
			logger.Logger.Errorf("event:%s: forward: read response fail: %s", event.AlertKey, err.Error())
			return
		}
	}

	if res.StatusCode != http.StatusOK {
		logger.Logger.Errorf("event:%s: forward: request fail: %s, response: %s", event.AlertKey, res.Status, string(body))
		return
	}

	logger.Logger.Debugf("event:%s: forward: done", event.AlertKey)
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
	sb.WriteString(event.Description)

	fmt.Println(sb.String())
}
