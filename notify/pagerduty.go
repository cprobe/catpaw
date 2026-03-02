package notify

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/cprobe/catpaw/config"
	"github.com/cprobe/catpaw/logger"
	"github.com/cprobe/catpaw/types"
)

type PagerDutyNotifier struct {
	cfg    *config.PagerDutyConfig
	url    string
	client *http.Client
}

func NewPagerDutyNotifier(cfg *config.PagerDutyConfig) *PagerDutyNotifier {
	return &PagerDutyNotifier{
		cfg: cfg,
		url: cfg.BaseUrl,
		client: &http.Client{
			Timeout: time.Duration(cfg.Timeout),
		},
	}
}

func (p *PagerDutyNotifier) Name() string { return "pagerduty" }

func (p *PagerDutyNotifier) Forward(event *types.Event) bool {
	bs, err := json.Marshal(p.toPayload(event))
	if err != nil {
		logger.Logger.Errorw("pagerduty: marshal fail",
			"event_key", event.AlertKey, "error", err.Error())
		return false
	}

	for attempt := 0; attempt <= p.cfg.MaxRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * time.Second)
			logger.Logger.Infow("pagerduty: retrying",
				"event_key", event.AlertKey, "attempt", attempt+1)
		}

		ok, retryable := p.doPost(event.AlertKey, bs)
		if ok {
			return true
		}
		if !retryable {
			return false
		}
	}

	logger.Logger.Errorw("pagerduty: all retries exhausted",
		"event_key", event.AlertKey, "max_retries", p.cfg.MaxRetries)
	return false
}

type pdEnvelope struct {
	RoutingKey  string    `json:"routing_key"`
	EventAction string    `json:"event_action"`
	DedupKey    string    `json:"dedup_key"`
	Payload     pdPayload `json:"payload"`
}

type pdPayload struct {
	Summary       string            `json:"summary"`
	Severity      string            `json:"severity"`
	Source        string            `json:"source"`
	Timestamp     string            `json:"timestamp"`
	CustomDetails map[string]string `json:"custom_details,omitempty"`
}

func (p *PagerDutyNotifier) toPayload(event *types.Event) *pdEnvelope {
	action := "trigger"
	if event.EventStatus == types.EventStatusOk {
		action = "resolve"
	}

	severity := "warning"
	if mapped, ok := p.cfg.SeverityMap[event.EventStatus]; ok {
		severity = mapped
	}

	source := event.Labels["from_hostip"]
	if source == "" {
		source = event.Labels["from_hostname"]
	}
	if source == "" {
		source = "catpaw"
	}

	summary := event.Description
	if event.DescriptionFormat == types.DescFormatMarkdown {
		summary = strings.TrimPrefix(summary, "[MD]")
	}
	if check := event.Labels["check"]; check != "" {
		summary = check + " " + summary
	}
	if len(summary) > 1024 {
		summary = summary[:1024]
	}

	details := make(map[string]string, len(event.Labels)+len(event.Attrs))
	for k, v := range event.Labels {
		details[k] = v
	}
	for k, v := range event.Attrs {
		details[k] = v
	}

	return &pdEnvelope{
		RoutingKey:  p.cfg.RoutingKey,
		EventAction: action,
		DedupKey:    event.AlertKey,
		Payload: pdPayload{
			Summary:       summary,
			Severity:      severity,
			Source:        source,
			Timestamp:     time.Unix(event.EventTime, 0).UTC().Format(time.RFC3339),
			CustomDetails: details,
		},
	}
}

func (p *PagerDutyNotifier) doPost(alertKey string, payload []byte) (bool, bool) {
	req, err := http.NewRequest("POST", p.url, bytes.NewReader(payload))
	if err != nil {
		logger.Logger.Errorw("pagerduty: new request fail",
			"event_key", alertKey, "error", err.Error())
		return false, false
	}
	req.Header.Set("Content-Type", "application/json")

	res, err := p.client.Do(req)
	if err != nil {
		logger.Logger.Errorw("pagerduty: do request fail",
			"event_key", alertKey, "error", err.Error())
		return false, true
	}

	var body []byte
	if res.Body != nil {
		defer res.Body.Close()
		body, err = io.ReadAll(res.Body)
		if err != nil {
			logger.Logger.Errorw("pagerduty: read response fail",
				"event_key", alertKey, "error", err.Error())
			return false, true
		}
	}

	if res.StatusCode == 202 {
		logger.Logger.Infow("pagerduty: forward completed",
			"event_key", alertKey, "response_status", res.StatusCode)
		return true, false
	}

	if res.StatusCode == 429 || res.StatusCode >= 500 {
		logger.Logger.Errorw("pagerduty: retryable error",
			"event_key", alertKey,
			"response_status", res.StatusCode,
			"response_body", string(body))
		return false, true
	}

	logger.Logger.Errorw("pagerduty: non-retryable error",
		"event_key", alertKey,
		"response_status", res.StatusCode,
		"response_body", string(body))
	return false, false
}
