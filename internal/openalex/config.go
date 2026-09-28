package openalex

import (
	"errors"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/kirbysss369/SciGraphDB/internal/devconfig"
)

// Config describes an OpenAlex API connection. The API key is never placed in a URL.
type Config struct {
	BaseURL string
	APIKey  string
	Timeout time.Duration
}

func LoadConfig() (Config, error) {
	if err := devconfig.Load(); err != nil {
		return Config{}, err
	}
	cfg := Config{
		BaseURL: os.Getenv("OPENALEX_BASE_URL"),
		APIKey:  os.Getenv("OPENALEX_API_KEY"),
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.openalex.org"
	}
	timeout := os.Getenv("OPENALEX_TIMEOUT")
	if timeout == "" {
		timeout = "10s"
	}
	var err error
	cfg.Timeout, err = time.ParseDuration(timeout)
	if err != nil || cfg.Timeout <= 0 {
		return Config{}, errors.New("OPENALEX_TIMEOUT must be a positive duration")
	}
	return cfg, cfg.validate()
}

func (cfg Config) validate() error {
	u, err := url.Parse(cfg.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") ||
		strings.ContainsAny(cfg.BaseURL, "\r\n") {
		return errors.New("OPENALEX_BASE_URL must be an http(s) origin without credentials, path, query, or fragment")
	}
	return nil
}
