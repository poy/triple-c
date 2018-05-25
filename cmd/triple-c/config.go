package main

import (
	"encoding/json"
	"strings"

	"code.cloudfoundry.org/go-envstruct"
)

type Config struct {
	Port              uint16          `env:"PORT, required, report"`
	VcapApplication   VcapApplication `env:"VCAP_APPLICATION, required, report"`
	RepoOwner         string          `env:"REPO_OWNER, required, report"`
	RepoName          string          `env:"REPO_NAME, required, report"`
	ClientID          string          `env:"CLIENT_ID, required"`
	RefreshToken      string          `env:"REFRESH_TOKEN, required"`
	SkipSSLValidation bool            `env:"SKIP_SSL_VALIDATION"`

	Command string `env:"COMMAND, required"`

	// Figured out via VcapApplication
	UAAAddr string
}

type VcapApplication struct {
	CAPIAddr      string `json:"cf_api"`
	ApplicationID string `json:"application_id"`
}

func (a *VcapApplication) UnmarshalEnv(data string) error {
	return json.Unmarshal([]byte(data), a)
}

func LoadConfig() (Config, error) {
	cfg := Config{
		Port: 8080,
	}

	if err := envstruct.Load(&cfg); err != nil {
		return Config{}, err
	}

	cfg.UAAAddr = strings.Replace(cfg.VcapApplication.CAPIAddr, "api", "uaa", 1)

	return cfg, nil
}
