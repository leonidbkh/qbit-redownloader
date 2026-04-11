package main

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Qbit struct {
		URL      string `yaml:"url"`
		Username string `yaml:"username"`
		Password string `yaml:"password"`
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
	if v := os.Getenv("QBIT_USERNAME"); v != "" {
		cfg.Qbit.Username = v
	}
	if v := os.Getenv("QBIT_PASSWORD"); v != "" {
		cfg.Qbit.Password = v
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
	if cfg.Prowlarr.URL == "" || cfg.Prowlarr.APIKey == "" {
		return nil, fmt.Errorf("prowlarr.url and prowlarr.api_key are required")
	}
	return cfg, nil
}
