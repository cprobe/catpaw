package secmod

import (
	"runtime"
	"testing"
)

func skipIfNotLinux(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("secmod is linux-only")
	}
}

// --- readSELinuxMode / readAppArmorStatus are thin wrappers around os.ReadFile,
// so we focus tests on Init validation and the normalizeSeverity helper. ---

func TestNormalizeSeverityDefaults(t *testing.T) {
	s := ""
	if err := normalizeSeverity(&s); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s != "Warning" {
		t.Fatalf("expected default 'Warning', got %q", s)
	}
}

func TestNormalizeSeverityAcceptsValid(t *testing.T) {
	for _, sev := range []string{"Info", "Warning", "Critical"} {
		s := sev
		if err := normalizeSeverity(&s); err != nil {
			t.Fatalf("should accept %q: %v", sev, err)
		}
	}
}

func TestNormalizeSeverityRejectsInvalid(t *testing.T) {
	for _, sev := range []string{"Ok", "Fatal", "error"} {
		s := sev
		if err := normalizeSeverity(&s); err == nil {
			t.Fatalf("should reject %q", sev)
		}
	}
}

// --- Init tests ---

func TestInitPlatformGuard(t *testing.T) {
	ins := &Instance{
		EnforceMode: EnforceModeCheck{Expect: "enforcing"},
	}
	err := ins.Init()
	if runtime.GOOS == "linux" && err != nil {
		t.Fatalf("should accept on Linux: %v", err)
	}
	if runtime.GOOS != "linux" && err == nil {
		t.Fatal("should reject on non-Linux")
	}
}

func TestInitAcceptsNothingConfigured(t *testing.T) {
	skipIfNotLinux(t)
	ins := &Instance{}
	if err := ins.Init(); err != nil {
		t.Fatalf("should accept when no checks configured: %v", err)
	}
}

func TestInitRejectsInvalidSELinuxExpect(t *testing.T) {
	skipIfNotLinux(t)
	ins := &Instance{
		EnforceMode: EnforceModeCheck{Expect: "enabled"},
	}
	if err := ins.Init(); err == nil {
		t.Fatal("should reject invalid SELinux expect value")
	}
}

func TestInitRejectsInvalidAppArmorExpect(t *testing.T) {
	skipIfNotLinux(t)
	ins := &Instance{
		AppArmor: AppArmorCheck{Expect: "enabled"},
	}
	if err := ins.Init(); err == nil {
		t.Fatal("should reject invalid AppArmor expect value")
	}
}

func TestInitRejectsInvalidSELinuxSeverity(t *testing.T) {
	skipIfNotLinux(t)
	ins := &Instance{
		EnforceMode: EnforceModeCheck{Expect: "enforcing", Severity: "Ok"},
	}
	if err := ins.Init(); err == nil {
		t.Fatal("should reject severity 'Ok'")
	}
}

func TestInitRejectsInvalidAppArmorSeverity(t *testing.T) {
	skipIfNotLinux(t)
	ins := &Instance{
		AppArmor: AppArmorCheck{Expect: "yes", Severity: "Fatal"},
	}
	if err := ins.Init(); err == nil {
		t.Fatal("should reject severity 'Fatal'")
	}
}

func TestInitAcceptsSELinuxOnly(t *testing.T) {
	skipIfNotLinux(t)
	for _, expect := range []string{"enforcing", "permissive", "disabled"} {
		ins := &Instance{
			EnforceMode: EnforceModeCheck{Expect: expect},
		}
		if err := ins.Init(); err != nil {
			t.Fatalf("should accept SELinux expect=%q: %v", expect, err)
		}
		if ins.EnforceMode.Severity != "Warning" {
			t.Fatalf("expected default severity 'Warning', got %q", ins.EnforceMode.Severity)
		}
	}
}

func TestInitAcceptsAppArmorOnly(t *testing.T) {
	skipIfNotLinux(t)
	for _, expect := range []string{"yes", "no"} {
		ins := &Instance{
			AppArmor: AppArmorCheck{Expect: expect},
		}
		if err := ins.Init(); err != nil {
			t.Fatalf("should accept AppArmor expect=%q: %v", expect, err)
		}
	}
}

func TestInitAcceptsBoth(t *testing.T) {
	skipIfNotLinux(t)
	ins := &Instance{
		EnforceMode: EnforceModeCheck{Expect: "enforcing", Severity: "Critical"},
		AppArmor:    AppArmorCheck{Expect: "yes", Severity: "Info"},
	}
	if err := ins.Init(); err != nil {
		t.Fatalf("should accept both checks: %v", err)
	}
}

func TestInitNormalizesExpectCase(t *testing.T) {
	skipIfNotLinux(t)
	ins := &Instance{
		EnforceMode: EnforceModeCheck{Expect: "Enforcing"},
		AppArmor:    AppArmorCheck{Expect: "Yes"},
	}
	if err := ins.Init(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ins.EnforceMode.Expect != "enforcing" {
		t.Fatalf("expected normalized 'enforcing', got %q", ins.EnforceMode.Expect)
	}
	if ins.AppArmor.Expect != "yes" {
		t.Fatalf("expected normalized 'yes', got %q", ins.AppArmor.Expect)
	}
}

func TestInitSkipsSELinuxValidationWhenEmpty(t *testing.T) {
	skipIfNotLinux(t)
	ins := &Instance{
		AppArmor: AppArmorCheck{Expect: "yes"},
	}
	if err := ins.Init(); err != nil {
		t.Fatalf("should skip SELinux validation when expect is empty: %v", err)
	}
}

func TestInitSkipsAppArmorValidationWhenEmpty(t *testing.T) {
	skipIfNotLinux(t)
	ins := &Instance{
		EnforceMode: EnforceModeCheck{Expect: "enforcing"},
	}
	if err := ins.Init(); err != nil {
		t.Fatalf("should skip AppArmor validation when expect is empty: %v", err)
	}
}
