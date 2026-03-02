package notify

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/cprobe/catpaw/config"
	"github.com/cprobe/catpaw/logger"
	"github.com/cprobe/catpaw/types"
)

const flashdutyAttrPrefix = "_attr_"

type FlashdutyNotifier struct {
	cfg    *config.FlashdutyConfig
	url    string
	client *http.Client
}

func NewFlashdutyNotifier(cfg *config.FlashdutyConfig) *FlashdutyNotifier {
	return &FlashdutyNotifier{
		cfg: cfg,
		url: cfg.BaseUrl + "?integration_key=" + cfg.IntegrationKey,
		client: &http.Client{
			Timeout: time.Duration(cfg.Timeout),
		},
	}
}

func (f *FlashdutyNotifier) Name() string { return "flashduty" }

func (f *FlashdutyNotifier) Forward(event *types.Event) bool {
	bs, err := json.Marshal(f.toPayload(event))
	if err != nil {
		logger.Logger.Errorw("flashduty: marshal fail",
			"event_key", event.AlertKey, "error", err.Error())
		return false
	}

	for attempt := 0; attempt <= f.cfg.MaxRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * time.Second)
			logger.Logger.Infow("flashduty: retrying",
				"event_key", event.AlertKey, "attempt", attempt+1)
		}

		ok, retryable := f.doPost(event.AlertKey, bs)
		if ok {
			return true
		}
		if !retryable {
			return false
		}
	}

	logger.Logger.Errorw("flashduty: all retries exhausted",
		"event_key", event.AlertKey, "max_retries", f.cfg.MaxRetries)
	return false
}

type flashDutyPayload struct {
	EventTime   int64             `json:"event_time"`
	EventStatus string            `json:"event_status"`
	AlertKey    string            `json:"alert_key"`
	Labels      map[string]string `json:"labels"`
	TitleRule   string            `json:"title_rule"`
	Description string            `json:"description"`
}

func (f *FlashdutyNotifier) toPayload(event *types.Event) *flashDutyPayload {
	labels := make(map[string]string, len(event.Labels)+len(event.Attrs))
	for k, v := range event.Labels {
		labels[k] = v
	}
	for k, v := range event.Attrs {
		labels[flashdutyAttrPrefix+k] = v
	}

	titleRule := "[TPL]${check} ${from_hostip}"
	if event.Labels["target"] != "" {
		titleRule = "[TPL]${check} ${from_hostip} ${target}"
	}

	return &flashDutyPayload{
		EventTime:   event.EventTime,
		EventStatus: event.EventStatus,
		AlertKey:    event.AlertKey,
		Labels:      labels,
		TitleRule:   titleRule,
		Description: event.Description,
	}
}

func (f *FlashdutyNotifier) doPost(alertKey string, payload []byte) (bool, bool) {
	req, err := http.NewRequest("POST", f.url, bytes.NewReader(payload))
	if err != nil {
		logger.Logger.Errorw("flashduty: new request fail",
			"event_key", alertKey, "error", err.Error())
		return false, false
	}
	req.Header.Set("Content-Type", "application/json")

	res, err := f.client.Do(req)
	if err != nil {
		logger.Logger.Errorw("flashduty: do request fail",
			"event_key", alertKey, "error", err.Error())
		return false, true
	}

	var body []byte
	if res.Body != nil {
		defer res.Body.Close()
		body, err = io.ReadAll(res.Body)
		if err != nil {
			logger.Logger.Errorw("flashduty: read response fail",
				"event_key", alertKey, "error", err.Error())
			return false, true
		}
	}

	if res.StatusCode >= 500 {
		logger.Logger.Errorw("flashduty: server error (retryable)",
			"event_key", alertKey,
			"response_status", res.StatusCode,
			"response_body", string(body))
		return false, true
	}

	if res.StatusCode >= 400 {
		logger.Logger.Errorw("flashduty: client error (non-retryable)",
			"event_key", alertKey,
			"response_status", res.StatusCode,
			"response_body", string(body))
		return false, false
	}

	logger.Logger.Infow("flashduty: forward completed",
		"event_key", alertKey,
		"request_payload", string(payload),
		"response_status", res.StatusCode,
		"response_body", string(body))
	return true, false
}
