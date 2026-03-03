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

type WebAPINotifier struct {
	cfg    *config.WebAPIConfig
	client *http.Client
}

func NewWebAPINotifier(cfg *config.WebAPIConfig) *WebAPINotifier {
	return &WebAPINotifier{
		cfg: cfg,
		client: &http.Client{
			Timeout: time.Duration(cfg.Timeout),
		},
	}
}

func (w *WebAPINotifier) Name() string { return "webapi" }

func (w *WebAPINotifier) Forward(event *types.Event) bool {
	bs, err := json.Marshal(event)
	if err != nil {
		logger.Logger.Errorw("webapi: marshal fail",
			"event_key", event.AlertKey, "error", err.Error())
		return false
	}

	for attempt := 0; attempt <= w.cfg.MaxRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * time.Second)
			logger.Logger.Infow("webapi: retrying",
				"event_key", event.AlertKey, "attempt", attempt+1)
		}

		ok, retryable := w.doRequest(event.AlertKey, bs)
		if ok {
			return true
		}
		if !retryable {
			return false
		}
	}

	logger.Logger.Errorw("webapi: all retries exhausted",
		"event_key", event.AlertKey, "max_retries", w.cfg.MaxRetries)
	return false
}

func (w *WebAPINotifier) doRequest(alertKey string, payload []byte) (ok bool, retryable bool) {
	req, err := http.NewRequest(w.cfg.Method, w.cfg.URL, bytes.NewReader(payload))
	if err != nil {
		logger.Logger.Errorw("webapi: new request fail",
			"event_key", alertKey, "error", err.Error())
		return false, false
	}

	req.Header.Set("Content-Type", "application/json")
	for k, v := range w.cfg.Headers {
		req.Header.Set(k, v)
	}

	res, err := w.client.Do(req)
	if err != nil {
		logger.Logger.Errorw("webapi: do request fail",
			"event_key", alertKey, "error", err.Error())
		return false, true
	}

	var body []byte
	if res.Body != nil {
		defer res.Body.Close()
		body, _ = io.ReadAll(io.LimitReader(res.Body, 4096))
	}

	if res.StatusCode >= 200 && res.StatusCode < 300 {
		logger.Logger.Infow("webapi: forward completed",
			"event_key", alertKey, "response_status", res.StatusCode)
		return true, false
	}

	if res.StatusCode == 429 || res.StatusCode >= 500 {
		logger.Logger.Errorw("webapi: retryable error",
			"event_key", alertKey,
			"response_status", res.StatusCode,
			"response_body", string(body))
		return false, true
	}

	logger.Logger.Errorw("webapi: non-retryable error",
		"event_key", alertKey,
		"response_status", res.StatusCode,
		"response_body", string(body))
	return false, false
}
