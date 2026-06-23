package client

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

type Config struct {
	ServerURL string `toml:"server_url"` // e.g. https://clip.example.com
	Token     string `toml:"token"`
	Source    string `toml:"source"`     // optional override; defaults to hostname
}

// DefaultPath returns ~/.clip/config.toml.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".clip", "config.toml"), nil
}

func Load(path string) (Config, error) {
	var c Config
	if _, err := toml.DecodeFile(path, &c); err != nil {
		return c, err
	}
	if c.ServerURL == "" {
		return c, errors.New("server_url missing")
	}
	if c.Token == "" {
		return c, errors.New("token missing")
	}
	if c.Source == "" {
		h, _ := os.Hostname()
		c.Source = h
	}
	return c, nil
}

// Save writes the config, creating the parent dir as needed.
func Save(path string, c Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	return toml.NewEncoder(f).Encode(c)
}
