package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bzon/spec-viewer/internal/config"
)

func TestDefaultConfig(t *testing.T) {
	c := config.Default()
	if c.Theme != "github-light" {
		t.Errorf("expected theme=github-light, got %q", c.Theme)
	}
	if c.Port != 0 {
		t.Errorf("expected port=0, got %d", c.Port)
	}
	if c.Host != "127.0.0.1" {
		t.Errorf("expected host=127.0.0.1, got %q", c.Host)
	}
	if c.NoOpen != false {
		t.Errorf("expected NoOpen=false, got %v", c.NoOpen)
	}
}

func TestLoadFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := "theme: monokai\nport: 8080\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	c, err := config.LoadFromFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Theme != "monokai" {
		t.Errorf("expected theme=monokai, got %q", c.Theme)
	}
	if c.Port != 8080 {
		t.Errorf("expected port=8080, got %d", c.Port)
	}
}

func TestLoadFromFileMissing(t *testing.T) {
	c, err := config.LoadFromFile("/nonexistent/path/config.yaml")
	if err != nil {
		t.Fatalf("expected no error for missing file, got %v", err)
	}
	def := config.Default()
	if c.Theme != def.Theme {
		t.Errorf("expected default theme, got %q", c.Theme)
	}
}

func TestMergeFlags(t *testing.T) {
	c, _ := config.LoadFromFile("/nonexistent/config.yaml")

	// flags override config when set
	c2 := c.MergeFlags(config.Flags{
		Theme: "dracula",
		Port:  9090,
		Host:  "0.0.0.0",
		NoOpen: true,
	})
	if c2.Theme != "dracula" {
		t.Errorf("expected theme=dracula, got %q", c2.Theme)
	}
	if c2.Port != 9090 {
		t.Errorf("expected port=9090, got %d", c2.Port)
	}
	if c2.Host != "0.0.0.0" {
		t.Errorf("expected host=0.0.0.0, got %q", c2.Host)
	}
	if !c2.NoOpen {
		t.Errorf("expected NoOpen=true")
	}

	// unset flags (zero values) don't override
	c3 := c2.MergeFlags(config.Flags{})
	if c3.Theme != "dracula" {
		t.Errorf("expected theme to remain dracula, got %q", c3.Theme)
	}
	if c3.Port != 9090 {
		t.Errorf("expected port to remain 9090, got %d", c3.Port)
	}
	if c3.Host != "0.0.0.0" {
		t.Errorf("expected host to remain 0.0.0.0, got %q", c3.Host)
	}
}
