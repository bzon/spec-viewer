package config

import (
	"errors"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config holds application configuration loaded from file and flags.
type Config struct {
	Theme  string `yaml:"theme"`
	Port   int    `yaml:"port"`
	Host   string `yaml:"host"`
	NoOpen bool   `yaml:"-"`
	Path   string `yaml:"-"`
}

// Flags holds CLI flag values that can override Config.
type Flags struct {
	Theme  string
	Port   int
	Host   string
	NoOpen bool
	Path   string
}

// Default returns a Config with default values.
func Default() Config {
	return Config{
		Theme:  "github-light",
		Port:   0,
		Host:   "127.0.0.1",
		NoOpen: false,
	}
}

// LoadFromFile reads a YAML config file and returns a Config.
// If the file does not exist, defaults are returned without error.
func LoadFromFile(path string) (Config, error) {
	c := Default()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return c, nil
		}
		return c, err
	}
	if err := yaml.Unmarshal(data, &c); err != nil {
		return c, err
	}
	return c, nil
}

// MergeFlags returns a new Config with non-zero flag values overriding the receiver.
func (c Config) MergeFlags(f Flags) Config {
	result := c
	if f.Theme != "" {
		result.Theme = f.Theme
	}
	if f.Port != 0 {
		result.Port = f.Port
	}
	if f.Host != "" {
		result.Host = f.Host
	}
	if f.NoOpen {
		result.NoOpen = f.NoOpen
	}
	if f.Path != "" {
		result.Path = f.Path
	}
	return result
}

// ConfigDir returns the default configuration directory.
func ConfigDir() string {
	return filepath.Join("~", ".config", "spec-viewer")
}

// DefaultConfigPath returns the default config file path.
func DefaultConfigPath() string {
	return filepath.Join(ConfigDir(), "config.yaml")
}

// CustomThemesDir returns the directory for custom themes.
func CustomThemesDir() string {
	return filepath.Join(ConfigDir(), "themes")
}
