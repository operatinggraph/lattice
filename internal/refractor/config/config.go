package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	NATS            NATSConfig `yaml:"nats"`
	CoreKVBucket    string     `yaml:"core_kv_bucket"`
	AdjKVBucket     string     `yaml:"adj_kv_bucket"`
	HealthKVBucket  string     `yaml:"health_kv_bucket"` // defaults to "materializer-health" if absent
}

type NATSConfig struct {
	URL             string `yaml:"url"`
	CredentialsFile string `yaml:"credentials_file"`
}

func Load(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open config file: %w", err)
	}
	defer func() { _ = f.Close() }()

	var cfg Config
	if err := yaml.NewDecoder(f).Decode(&cfg); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}

	if v := os.Getenv("NATS_URL"); v != "" {
		cfg.NATS.URL = v
	}
	if v := os.Getenv("NATS_CREDENTIALS_FILE"); v != "" {
		cfg.NATS.CredentialsFile = v
	}

	if cfg.NATS.URL == "" {
		return nil, fmt.Errorf("nats.url is required (set via config file or NATS_URL env var)")
	}
	if cfg.CoreKVBucket == "" {
		return nil, fmt.Errorf("core_kv_bucket is required")
	}
	if cfg.AdjKVBucket == "" {
		return nil, fmt.Errorf("adj_kv_bucket is required")
	}
	if cfg.HealthKVBucket == "" {
		cfg.HealthKVBucket = "materializer-health"
	}

	return &cfg, nil
}
