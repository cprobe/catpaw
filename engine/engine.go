package engine

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cprobe/catpaw/config"
	"github.com/cprobe/catpaw/diagnose"
	"github.com/cprobe/catpaw/logger"
	"github.com/cprobe/catpaw/notify"
	"github.com/cprobe/catpaw/pkg/safe"
	"github.com/cprobe/catpaw/plugins"
	"github.com/cprobe/catpaw/types"
	"github.com/toolkits/pkg/str"
)

var (
	diagnosedKeys sync.Map // AlertKey → struct{}: tracks alerts that already triggered a diagnosis
)

func PushRawEvents(pluginName string, pluginObj plugins.Plugin, ins plugins.Instance, queue *safe.Queue[*types.Event]) {
	if queue.Len() == 0 {
		return
	}

	now := time.Now().Unix()
	events := queue.PopBackAll()

	for i := range events {
		if events[i] == nil {
			continue
		}

		err := clean(events[i], now, pluginName, pluginObj, ins)
		if err != nil {
			logger.Logger.Errorw("clean raw event fail",
				"error", err.Error(),
				"event", events[i],
			)
			continue
		}

		logger.Logger.Debugw("raw data received",
			"event_key", events[i].AlertKey,
			"event", events[i],
		)

		if ins.GetAlerting().Disabled {
			continue
		}

		if events[i].EventStatus == types.EventStatusOk {
			handleRecoveryEvent(ins, events[i])
		} else {
			if handleAlertEvent(ins, events[i]) {
				mayTriggerDiagnose(events[i], pluginName, ins)
			}
		}
	}
}

func mayTriggerDiagnose(event *types.Event, pluginName string, ins plugins.Instance) {
	agg := diagnose.GlobalAggregator()
	if agg == nil {
		return
	}
	if _, already := diagnosedKeys.Load(event.AlertKey); already {
		return
	}
	snapshot := diagnose.ExtractCheckSnapshot(event)
	diagCfg := ins.GetDiagnoseConfig()
	agg.Submit(event, snapshot, pluginName, ins, diagCfg)
	diagnosedKeys.Store(event.AlertKey, struct{}{})
}

// 处理恢复事件
func handleRecoveryEvent(ins plugins.Instance, event *types.Event) {
	diagnosedKeys.Delete(event.AlertKey)

	old := Events.Get(event.AlertKey)
	if old == nil {
		// 之前没有产生Event，当下的情况也是正常的，这是大多数场景，忽略即可，无需做任何处理
		return
	}

	// 之前产生了告警，现在恢复了，事件就可以从缓存删除了
	Events.Del(old.AlertKey)

	// 不过，也得看具体 alerting 的配置，如果不需要发送恢复通知，则忽略
	if !ins.GetAlerting().DisableRecoveryNotification && old.LastSent > 0 {
		notify.Forward(event)
	}
}

// 处理告警事件，返回 true 表示告警已实际发送到 notify 后端。
func handleAlertEvent(ins plugins.Instance, event *types.Event) bool {
	alerting := ins.GetAlerting()
	old := Events.Get(event.AlertKey)
	if old == nil {
		// 第一次产生告警事件
		event.FirstFireTime = event.EventTime

		// 无论如何，这个事件都得缓存起来
		Events.Set(event)

		// 要不要发？分两种情况。ForDuration 是 0 则立马发，否则等待 ForDuration 时间后再发
		if alerting.ForDuration == 0 {
			if notify.Forward(event) {
				event.LastSent = event.EventTime
				event.NotifyCount++
				return true
			}
		}

		return false
	}

	// old != nil 这已经不是第一次产生告警事件了
	// 如果 ForDuration 没有满足，则不能继续发送
	if alerting.ForDuration > 0 && event.EventTime-old.FirstFireTime < int64(alerting.ForDuration/config.Duration(time.Second)) {
		return false
	}

	// ForDuration 满足了，可以继续发送了
	// 首先看是否达到最大发送次数
	if alerting.RepeatNumber > 0 && old.NotifyCount >= int64(alerting.RepeatNumber) {
		return false
	}

	// 其次看发送频率，不能发的太快了
	if alerting.RepeatInterval > 0 && event.EventTime-old.LastSent < int64(alerting.RepeatInterval/config.Duration(time.Second)) {
		return false
	}

	// 最后，可以发了
	event.FirstFireTime = old.FirstFireTime
	event.NotifyCount = old.NotifyCount + 1
	if notify.Forward(event) {
		event.LastSent = event.EventTime
		Events.Set(event)
		return true
	}
	return false
}

func clean(event *types.Event, now int64, pluginName string, pluginObj plugins.Plugin, ins plugins.Instance) error {
	if event.EventTime == 0 {
		event.EventTime = now
	}

	if !types.EventStatusValid(event.EventStatus) {
		return fmt.Errorf("invalid event_status: %s", event.EventStatus)
	}

	if event.Labels == nil {
		event.Labels = make(map[string]string)
	}

	// append label: from_plugin
	event.Labels["from_plugin"] = pluginName

	// append label from plugin
	plLabels := pluginObj.GetLabels()
	for k, v := range plLabels {
		event.Labels[k] = v
	}

	// append label from instance
	insLabels := ins.GetLabels()
	for k, v := range insLabels {
		event.Labels[k] = v
	}

	// append label: global labels with live identity values
	for key, val := range config.Config.Global.Labels {
		event.Labels[key] = val
	}
	event.Labels["from_hostip"] = config.AgentIP()
	event.Labels["from_hostname"] = config.AgentHostname()

	keys := make([]string, 0, len(event.Labels))
	for k := range event.Labels {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	var sb strings.Builder
	for _, k := range keys {
		sb.WriteString(k)
		sb.WriteString(":")
		sb.WriteString(event.Labels[k])
		sb.WriteString(":")
	}

	event.AlertKey = str.MD5(sb.String())

	return nil
}

