package zombie

import (
	"testing"
)

func intPtr(v int) *int { return &v }

func TestInitRequiresThreshold(t *testing.T) {
	ins := &Instance{}
	if err := ins.Init(); err == nil {
		t.Fatal("should reject when no threshold configured")
	}
}

func TestInitAcceptsWarnOnly(t *testing.T) {
	ins := &Instance{WarnGt: intPtr(0)}
	if err := ins.Init(); err != nil {
		t.Fatalf("warn_gt only should be accepted: %v", err)
	}
}

func TestInitAcceptsCriticalOnly(t *testing.T) {
	ins := &Instance{CriticalGt: intPtr(10)}
	if err := ins.Init(); err != nil {
		t.Fatalf("critical_gt only should be accepted: %v", err)
	}
}

func TestInitAcceptsBoth(t *testing.T) {
	ins := &Instance{WarnGt: intPtr(0), CriticalGt: intPtr(20)}
	if err := ins.Init(); err != nil {
		t.Fatalf("both thresholds should be accepted: %v", err)
	}
}

func TestInitRejectsNegativeWarn(t *testing.T) {
	ins := &Instance{WarnGt: intPtr(-1)}
	if err := ins.Init(); err == nil {
		t.Fatal("should reject negative warn_gt")
	}
}

func TestInitRejectsNegativeCritical(t *testing.T) {
	ins := &Instance{CriticalGt: intPtr(-5)}
	if err := ins.Init(); err == nil {
		t.Fatal("should reject negative critical_gt")
	}
}

func TestInitRejectsWarnGreaterThanCritical(t *testing.T) {
	ins := &Instance{WarnGt: intPtr(30), CriticalGt: intPtr(10)}
	if err := ins.Init(); err == nil {
		t.Fatal("should reject warn_gt > critical_gt")
	}
}

func TestNewEvent(t *testing.T) {
	ins := &Instance{WarnGt: intPtr(0)}
	_ = ins.Init()
	event := ins.newEvent()

	if event.Labels["check"] != "zombie::count" {
		t.Fatalf("expected check %q, got %q", "zombie::count", event.Labels["check"])
	}
	if event.Labels["target"] != "system" {
		t.Fatalf("expected target %q, got %q", "system", event.Labels["target"])
	}
}

func TestNewEventCustomTitleRule(t *testing.T) {
	ins := &Instance{WarnGt: intPtr(0), TitleRule: "[check] on [target]"}
	_ = ins.Init()
	event := ins.newEvent()

	if event.Labels["check"] != "zombie::count" {
		t.Fatalf("expected check %q, got %q", "zombie::count", event.Labels["check"])
	}
}
