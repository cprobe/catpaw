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
		"00.local.toml",
		LocalOverrideConfigFile,
		"a-first.toml",
		DefaultConfigFile,
	}

	got := orderedConfigFiles(files)
	want := []string{
		DefaultConfigFile,
		"a-first.toml",
		"z-last.toml",
		"00.local.toml",
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

func TestLoadConfigByDirLocalTomlOverridesRegularToml(t *testing.T) {
	dir := t.TempDir()

	writeTestFile(t, filepath.Join(dir, "99.toml"), `
[app]
name = "base"
`)
	writeTestFile(t, filepath.Join(dir, "00.local.toml"), `
[app]
name = "local"
`)

	var cfg struct {
		App struct {
			Name string `toml:"name"`
		} `toml:"app"`
	}
	if err := LoadConfigByDir(dir, &cfg); err != nil {
		t.Fatalf("LoadConfigByDir() error = %v", err)
	}
	if cfg.App.Name != "local" {
		t.Fatalf("App.Name = %q, want local", cfg.App.Name)
	}
}

func TestLoadConfigByDirRegularTomlsKeepSortedOrderBeforeLocal(t *testing.T) {
	dir := t.TempDir()

	writeTestFile(t, filepath.Join(dir, "01.toml"), `
[app]
name = "first"
`)
	writeTestFile(t, filepath.Join(dir, "02.toml"), `
[app]
name = "second"
`)

	var cfg struct {
		App struct {
			Name string `toml:"name"`
		} `toml:"app"`
	}
	if err := LoadConfigByDir(dir, &cfg); err != nil {
		t.Fatalf("LoadConfigByDir() error = %v", err)
	}
	if cfg.App.Name != "second" {
		t.Fatalf("App.Name = %q, want second", cfg.App.Name)
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write file %s: %v", path, err)
	}
}
