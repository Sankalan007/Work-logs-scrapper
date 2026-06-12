package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
)

// Config represents the stored CLI credentials and settings.
type Config struct {
	GitLabInstance string `json:"gitlab_instance"`
	GitLabToken    string `json:"gitlab_token"`
	GeminiAPIKey   string `json:"gemini_api_key"`
	DefaultModel   string `json:"default_model,omitempty"`
}

// GetConfigPath returns the absolute path to the local configuration file (~/.gitlab-worklogs.json).
func GetConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".gitlab-worklogs.json"), nil
}

// LoadConfig reads the configuration file.
// If the file does not exist, it returns an empty Config and nil error.
func LoadConfig() (*Config, error) {
	path, err := GetConfigPath()
	if err != nil {
		return nil, err
	}

	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{}, nil
		}
		return nil, err
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// SaveConfig writes the configuration back to the configuration file.
func SaveConfig(cfg *Config) error {
	if cfg == nil {
		return errors.New("cannot save nil configuration")
	}

	path, err := GetConfigPath()
	if err != nil {
		return err
	}

	// Create directories if they do not exist (though home directory should)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0600)
}
