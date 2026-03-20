package cfg

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigByDirLocalTomlOverridesRegularToml(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "99.toml"), `
[app]
name = "base"
`)
	writeFile(t, filepath.Join(dir, "00.local.toml"), `
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
	writeFile(t, filepath.Join(dir, "01.toml"), `
[app]
name = "first"
`)
	writeFile(t, filepath.Join(dir, "02.toml"), `
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

func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("os.WriteFile(%s): %v", path, err)
	}
}
