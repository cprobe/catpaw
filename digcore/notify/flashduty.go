package notify

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/cprobe/catpaw/digcore/config"
	"github.com/cprobe/catpaw/digcore/logger"
	"github.com/cprobe/catpaw/digcore/types"
)

const flashdutyAttrPrefix = "_attr_"

type FlashdutyNotifier struct {
	cfg        *config.FlashdutyConfig
	url        string
	commentURL string
	client     *http.Client
}

func NewFlashdutyNotifier(cfg *config.FlashdutyConfig) *FlashdutyNotifier {
	return &FlashdutyNotifier{
		cfg:        cfg,
		url:        buildFlashdutyRequestURL(cfg.BaseUrl, cfg.IntegrationKey),
		commentURL: buildFlashdutyCommentURL(cfg.BaseUrl, cfg.IntegrationKey),
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

		ok, retryable := f.doPost(event.AlertKey, f.url, bs)
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

func (f *FlashdutyNotifier) Comment(alertKey, comment string) bool {
	comment = strings.TrimSpace(comment)
	if comment == "" {
		logger.Logger.Warnw("flashduty: empty comment skipped", "alert_key", alertKey)
		return false
	}

	logger.Logger.Infow("flashduty: sending comment",
		"alert_key", alertKey, "comment_url", f.commentURL, "comment_len", len(comment))

	bs, err := json.Marshal(flashdutyCommentPayload{
		AlertKey: alertKey,
		Comment:  comment,
	})
	if err != nil {
		logger.Logger.Errorw("flashduty: marshal comment fail",
			"alert_key", alertKey, "error", err.Error())
		return false
	}

	for attempt := 0; attempt <= f.cfg.MaxRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * time.Second)
			logger.Logger.Infow("flashduty: retrying comment",
				"alert_key", alertKey, "attempt", attempt+1)
		}

		ok, retryable := f.doPost(alertKey, f.commentURL, bs)
		if ok {
			logger.Logger.Infow("flashduty: comment sent successfully",
				"alert_key", alertKey)
			return true
		}
		if !retryable {
			return false
		}
	}

	logger.Logger.Errorw("flashduty: comment retries exhausted",
		"alert_key", alertKey, "max_retries", f.cfg.MaxRetries)
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

type flashdutyCommentPayload struct {
	AlertKey string `json:"alert_key"`
	Comment  string `json:"comment"`
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

	desc := event.Description
	if event.DescriptionFormat == types.DescFormatMarkdown {
		desc = "[MD]" + desc
	}

	return &flashDutyPayload{
		EventTime:   event.EventTime,
		EventStatus: event.EventStatus,
		AlertKey:    event.AlertKey,
		Labels:      labels,
		TitleRule:   titleRule,
		Description: desc,
	}
}

func buildFlashdutyRequestURL(baseURL, integrationKey string) string {
	u, err := url.Parse(baseURL)
	if err != nil {
		return baseURL + "?integration_key=" + integrationKey
	}
	q := u.Query()
	q.Set("integration_key", integrationKey)
	u.RawQuery = q.Encode()
	return u.String()
}

func buildFlashdutyCommentURL(baseURL, integrationKey string) string {
	u, err := url.Parse(baseURL)
	if err != nil {
		return "/push/incident/comment-by-alert?integration_key=" + integrationKey
	}

	u.Path = deriveFlashdutyCommentPath(u.Path)
	u.RawPath = ""
	q := u.Query()
	q.Set("integration_key", integrationKey)
	u.RawQuery = q.Encode()
	return u.String()
}

func deriveFlashdutyCommentPath(alertPath string) string {
	const alertPathMarker = "/event/push/alert/"
	const commentPath = "/push/incident/comment-by-alert"

	if idx := strings.Index(alertPath, alertPathMarker); idx >= 0 {
		prefix := strings.TrimRight(alertPath[:idx], "/")
		if prefix == "" {
			return commentPath
		}
		return prefix + commentPath
	}
	return commentPath
}

func (f *FlashdutyNotifier) doPost(alertKey, requestURL string, payload []byte) (bool, bool) {
	req, err := http.NewRequest("POST", requestURL, bytes.NewReader(payload))
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

	logger.Logger.Debugw("flashduty: forward completed",
		"event_key", alertKey,
		"request_url", requestURL,
		"request_payload", string(payload),
		"response_status", res.StatusCode,
		"response_body", string(body))
	return true, false
}
