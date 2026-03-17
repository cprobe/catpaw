package cfg

import (
	"os"
	"path/filepath"
	"testing"
)

type testConfig struct {
	Log struct {
		Level string `toml:"level"`
	} `toml:"log"`
	Notify struct {
		Console struct {
			Enabled bool `toml:"enabled"`
		} `toml:"console"`
	} `toml:"notify"`
}

func TestOrderedConfigFiles(t *testing.T) {
	files := []string{
		"z-last.toml",
		LocalOverrideConfigFile,
		"a-first.toml",
		DefaultConfigFile,
	}

	got := orderedConfigFiles(files)
	want := []string{
		DefaultConfigFile,
		"a-first.toml",
		"z-last.toml",
		LocalOverrideConfigFile,
	}

	if len(got) != len(want) {
		t.Fatalf("ordered files len=%d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ordered files[%d]=%q, want %q", i, got[i], want[i])
		}
	}
}

func TestLoadConfigByDirLoadsLocalOverrideLast(t *testing.T) {
	dir := t.TempDir()

	writeTestFile(t, filepath.Join(dir, LocalOverrideConfigFile), `
[log]
level = "warn"
`)
	writeTestFile(t, filepath.Join(dir, "extra.toml"), `
[notify.console]
enabled = true

[log]
level = "debug"
`)
	writeTestFile(t, filepath.Join(dir, DefaultConfigFile), `
[log]
level = "info"
`)

	var cfg testConfig
	if err := LoadConfigByDir(dir, &cfg); err != nil {
		t.Fatalf("LoadConfigByDir() error = %v", err)
	}

	if cfg.Log.Level != "warn" {
		t.Fatalf("log.level = %q, want %q", cfg.Log.Level, "warn")
	}
	if !cfg.Notify.Console.Enabled {
		t.Fatal("notify.console.enabled = false, want true")
	}
}

func TestLoadConfigByDirWithoutLocalOverride(t *testing.T) {
	dir := t.TempDir()

	writeTestFile(t, filepath.Join(dir, "extra.toml"), `
[log]
level = "debug"
`)
	writeTestFile(t, filepath.Join(dir, DefaultConfigFile), `
[log]
level = "info"
`)

	var cfg testConfig
	if err := LoadConfigByDir(dir, &cfg); err != nil {
		t.Fatalf("LoadConfigByDir() error = %v", err)
	}

	if cfg.Log.Level != "debug" {
		t.Fatalf("log.level = %q, want %q", cfg.Log.Level, "debug")
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write file %s: %v", path, err)
	}
}
