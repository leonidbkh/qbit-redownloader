package main

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Qbit struct {
		URL        string `yaml:"url"`
		APIKey     string `yaml:"api_key"`
		APIKeyFile string `yaml:"api_key_file"`
	} `yaml:"qbit"`
	Prowlarr struct {
		URL    string `yaml:"url"`
		APIKey string `yaml:"api_key"`
	} `yaml:"prowlarr"`
}

func LoadConfig(path string) (*Config, error) {
	cfg := &Config{}
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read config: %w", err)
		}
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parse config: %w", err)
		}
	}
	if v := os.Getenv("QBIT_URL"); v != "" {
		cfg.Qbit.URL = v
	}
	if v := os.Getenv("QBIT_API_KEY"); v != "" {
		cfg.Qbit.APIKey = v
	}
	if v := os.Getenv("QBIT_API_KEY_FILE"); v != "" {
		cfg.Qbit.APIKeyFile = v
	}
	if v := os.Getenv("PROWLARR_URL"); v != "" {
		cfg.Prowlarr.URL = v
	}
	if v := os.Getenv("PROWLARR_API_KEY"); v != "" {
		cfg.Prowlarr.APIKey = v
	}
	if cfg.Qbit.URL == "" {
		return nil, fmt.Errorf("qbit.url is required")
	}
	if cfg.Qbit.APIKey == "" && cfg.Qbit.APIKeyFile != "" {
		apiKey, err := readQbitAPIKeyFile(cfg.Qbit.APIKeyFile)
		if err != nil {
			return nil, err
		}
		cfg.Qbit.APIKey = apiKey
	}
	if cfg.Qbit.APIKey == "" {
		return nil, fmt.Errorf("qbit.api_key is required")
	}
	if cfg.Prowlarr.URL == "" || cfg.Prowlarr.APIKey == "" {
		return nil, fmt.Errorf("prowlarr.url and prowlarr.api_key are required")
	}
	return cfg, nil
}

func readQbitAPIKeyFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read qbit api key file: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if key, ok := strings.CutPrefix(line, `WebUI\APIKey=`); ok {
			return strings.TrimSpace(key), nil
		}
	}
	key := strings.TrimSpace(string(data))
	if key == "" {
		return "", fmt.Errorf("qbit api key file is empty")
	}
	return key, nil
}
