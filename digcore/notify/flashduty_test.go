package notify

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cprobe/catpaw/digcore/config"
	"github.com/cprobe/catpaw/digcore/logger"
	"go.uber.org/zap"
)

func initNotifyTestLogger() {
	if logger.Logger == nil {
		logger.Logger = zap.NewNop().Sugar()
	}
}

func TestBuildFlashdutyCommentURL_DefaultEndpoint(t *testing.T) {
	got := buildFlashdutyCommentURL("https://api.flashcat.cloud/event/push/alert/standard", "test-key")
	want := "https://api.flashcat.cloud/push/incident/comment-by-alert?integration_key=test-key"
	if got != want {
		t.Fatalf("buildFlashdutyCommentURL() = %q, want %q", got, want)
	}
}

func TestBuildFlashdutyCommentURL_PreservesPrefix(t *testing.T) {
	got := buildFlashdutyCommentURL("https://engine.example.com/proxy/event/push/alert/standard", "test-key")
	want := "https://engine.example.com/proxy/push/incident/comment-by-alert?integration_key=test-key"
	if got != want {
		t.Fatalf("buildFlashdutyCommentURL() = %q, want %q", got, want)
	}
}

func TestFlashdutyNotifierComment(t *testing.T) {
	initNotifyTestLogger()

	var gotPath string
	var gotIntegrationKey string
	var gotPayload flashdutyCommentPayload

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotIntegrationKey = r.URL.Query().Get("integration_key")
		if err := json.NewDecoder(r.Body).Decode(&gotPayload); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	n := NewFlashdutyNotifier(&config.FlashdutyConfig{
		IntegrationKey: "test-key",
		BaseUrl:        srv.URL + "/event/push/alert/standard",
		Timeout:        config.Duration(2 * time.Second),
		MaxRetries:     0,
	})

	if ok := n.Comment("alert-123", "  diagnosis report  "); !ok {
		t.Fatal("Comment() = false, want true")
	}

	if gotPath != "/push/incident/comment-by-alert" {
		t.Fatalf("request path = %q, want %q", gotPath, "/push/incident/comment-by-alert")
	}
	if gotIntegrationKey != "test-key" {
		t.Fatalf("integration_key = %q, want %q", gotIntegrationKey, "test-key")
	}
	if gotPayload.AlertKey != "alert-123" {
		t.Fatalf("alert_key = %q, want %q", gotPayload.AlertKey, "alert-123")
	}
	if gotPayload.Comment != "diagnosis report" {
		t.Fatalf("comment = %q, want %q", gotPayload.Comment, "diagnosis report")
	}
}
