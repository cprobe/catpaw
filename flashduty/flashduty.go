package flashduty

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/cprobe/catpaw/config"
	"github.com/cprobe/catpaw/logger"
	"github.com/cprobe/catpaw/types"
)

// Forward sends an event to FlashDuty with retry. In test mode it prints
// to stdout instead. Returns true on success.
func Forward(event *types.Event) bool {
	if config.Config.TestMode {
		PrintStdout(event)
		return true
	}

	if config.Config.Flashduty.Url == "" {
		logger.Logger.Warnw("forward: flashduty url not configured, event dropped",
			"event_key", event.AlertKey,
		)
		return false
	}

	bs, err := json.Marshal(event)
	if err != nil {
		logger.Logger.Errorw("forward: marshal fail",
			"event_key", event.AlertKey,
			"error", err.Error(),
		)
		return false
	}

	maxRetries := config.Config.Flashduty.MaxRetries
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * time.Second)
			logger.Logger.Infow("forward: retrying",
				"event_key", event.AlertKey,
				"attempt", attempt+1,
			)
		}

		ok, retryable := doForward(event.AlertKey, bs)
		if ok {
			return true
		}
		if !retryable {
			return false
		}
	}

	logger.Logger.Errorw("forward: all retries exhausted",
		"event_key", event.AlertKey,
		"max_retries", maxRetries,
	)
	return false
}

// doForward sends a single HTTP request and returns (success, retryable).
// Network errors and 5xx are retryable; 4xx and marshal/URL errors are not.
func doForward(alertKey string, payload []byte) (bool, bool) {
	req, err := http.NewRequest("POST", config.Config.Flashduty.Url, bytes.NewReader(payload))
	if err != nil {
		logger.Logger.Errorw("forward: new request fail",
			"event_key", alertKey,
			"error", err.Error(),
		)
		return false, false
	}

	req.Header.Set("Content-Type", "application/json")

	res, err := config.Config.Flashduty.Client.Do(req)
	if err != nil {
		logger.Logger.Errorw("forward: do request fail",
			"event_key", alertKey,
			"error", err.Error(),
		)
		return false, true
	}

	var body []byte
	if res.Body != nil {
		defer res.Body.Close()
		body, err = io.ReadAll(res.Body)
		if err != nil {
			logger.Logger.Errorw("forward: read response fail",
				"event_key", alertKey,
				"error", err.Error(),
			)
			return false, true
		}
	}

	if res.StatusCode >= 500 {
		logger.Logger.Errorw("forward: server error (retryable)",
			"event_key", alertKey,
			"response_status", res.StatusCode,
			"response_body", string(body),
		)
		return false, true
	}

	if res.StatusCode >= 400 {
		logger.Logger.Errorw("forward: client error (non-retryable)",
			"event_key", alertKey,
			"response_status", res.StatusCode,
			"response_body", string(body),
		)
		return false, false
	}

	logger.Logger.Infow("forward completed",
		"event_key", alertKey,
		"request_payload", string(payload),
		"response_status", res.StatusCode,
		"response_body", string(body),
	)
	return true, false
}

// PrintStdout prints a human-readable event line to stdout (for test mode).
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

	sb.WriteString(" ")
	sb.WriteString(event.TitleRule)
	sb.WriteString(" ")
	sb.WriteString(event.Description)

	fmt.Println(sb.String())
}
