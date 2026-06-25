// Copyright © 2026 Harness Inc.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v3"

	"github.com/harness/harness-cli/pkg/hbase"
)

// AuthType identifies how the token in the credentials file was obtained.
// Empty string (existing profiles) is treated as AuthTypePAT.
type AuthType = string

const (
	AuthTypePAT = "pat" // default; omitted from YAML for existing profiles
	AuthTypeSSO = "sso" // OAuth2 JWT obtained via browser login
)

type Profile struct {
	APIUrl      string   `yaml:"api_url"`
	UIUrl       string   `yaml:"ui_url,omitempty"` // Harness UI base URL; set for SSO profiles from JWT subdomain
	AccountID   string   `yaml:"account_id"`
	OrgID       string   `yaml:"org_id,omitempty"`
	ProjectID   string   `yaml:"project_id,omitempty"`
	RegistryURL string   `yaml:"registry_url,omitempty"`
	AuthType    AuthType `yaml:"auth_type,omitempty"` // omitted for existing PAT profiles
}

type Config struct {
	Profiles         map[string]*Profile `yaml:"profiles"`
	DisableTelemetry bool                `yaml:"disable_telemetry,omitempty"`
}

func LoadConfig() (*Config, error) {
	path := hbase.GetConfigFilePath()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &Config{Profiles: make(map[string]*Profile)}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	if cfg.Profiles == nil {
		cfg.Profiles = make(map[string]*Profile)
	}
	return &cfg, nil
}

func SaveConfig(cfg *Config) error {
	path := hbase.GetConfigFilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}
	return os.WriteFile(path, data, 0600)
}
