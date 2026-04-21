package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Technitium TechnitiumConfig `yaml:"technitium"`
	Incus      IncusConfig      `yaml:"incus"`
	DNS        DNSConfig        `yaml:"dns"`
	Reconciler ReconcilerConfig `yaml:"reconciler"`
	Log        LogConfig        `yaml:"log"`
}

type TechnitiumConfig struct {
	APIBase string        `yaml:"api_base"`
	Token   string        `yaml:"token"`
	Timeout time.Duration `yaml:"timeout"`
}

type IncusConfig struct {
	SocketPath     string        `yaml:"socket_path"`
	DNSProfile     string        `yaml:"dns_profile"`
	Interface      string        `yaml:"interface"`
	IPWaitTimeout  time.Duration `yaml:"ip_wait_timeout"`
	IPPollInterval time.Duration `yaml:"ip_poll_interval"`
}

type DNSConfig struct {
	Zone string `yaml:"zone"`
	TTL  int    `yaml:"ttl"`
}

type ReconcilerConfig struct {
	Interval time.Duration `yaml:"interval"`
}

type LogConfig struct {
	Level string `yaml:"level"`
}

func Load(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("config file couldn't open (%s): %w", path, err)
	}
	defer f.Close()

	cfg := defaults()
	if err := yaml.NewDecoder(f).Decode(&cfg); err != nil {
		return nil, fmt.Errorf("config parse error: %w", err)
	}

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("config correction error: %w", err)
	}

	return cfg, nil
}

func defaults() *Config {
	return &Config{
		Technitium: TechnitiumConfig{
			Timeout: 15 * time.Second,
		},
		Incus: IncusConfig{
			DNSProfile:     "dns-enabled",
			Interface:      "eth0",
			IPWaitTimeout:  60 * time.Second,
			IPPollInterval: 3 * time.Second,
		},
		DNS: DNSConfig{
			TTL: 300,
		},
		Reconciler: ReconcilerConfig{
			Interval: 5 * time.Minute,
		},
		Log: LogConfig{
			Level: "info",
		},
	}
}

func (c *Config) validate() error {
	if c.Technitium.APIBase == "" {
		return fmt.Errorf("technitium.api_base boş olamaz")
	}
	if c.Technitium.Token == "" {
		return fmt.Errorf("technitium.token boş olamaz")
	}
	if c.DNS.Zone == "" {
		return fmt.Errorf("dns.zone boş olamaz")
	}
	return nil
}
