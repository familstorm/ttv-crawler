package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const defaultStartURL = "https://tangthuvien.org/truyen?sort=rate&order=desc"

type Config struct {
	DatabaseURL       string
	StartURL          string
	CatalogMaxPage    int
	RequestInterval   time.Duration
	RequestJitter     time.Duration
	Workers           int
	HTTPTimeout       time.Duration
	HTTPRetries       int
	MaxResponseBytes  int64
	UserAgent         string
	BrowserExecutable string
	RobotsFailOpen    bool
	MaxJobAttempts    int
	IdleExitAfter     time.Duration
	LogLevel          string
}

func Load() (cfg Config, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			cfg = Config{}
			err = fmt.Errorf("cấu hình không hợp lệ: %v", recovered)
		}
	}()
	c := Config{
		DatabaseURL:       env("DATABASE_URL", "postgres://ttv:ttv@localhost:5432/ttv?sslmode=disable"),
		StartURL:          env("START_URL", defaultStartURL),
		CatalogMaxPage:    intEnv("CATALOG_MAX_PAGE", 284),
		RequestInterval:   durationEnv("REQUEST_INTERVAL", 3*time.Second),
		RequestJitter:     durationEnv("REQUEST_JITTER", 1500*time.Millisecond),
		Workers:           intEnv("WORKERS", 1),
		HTTPTimeout:       durationEnv("HTTP_TIMEOUT", 30*time.Second),
		HTTPRetries:       intEnv("HTTP_RETRIES", 3),
		MaxResponseBytes:  int64Env("MAX_RESPONSE_BYTES", 8*1024*1024),
		UserAgent:         env("USER_AGENT", "TTVPersonalArchiver/1.0 (+personal offline reading; rate-limited)"),
		BrowserExecutable: env("BROWSER_EXECUTABLE", ""),
		RobotsFailOpen:    boolEnv("ROBOTS_FAIL_OPEN", true),
		MaxJobAttempts:    intEnv("MAX_JOB_ATTEMPTS", 8),
		IdleExitAfter:     durationEnv("IDLE_EXIT_AFTER", 0),
		LogLevel:          strings.ToLower(env("LOG_LEVEL", "info")),
	}

	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// LoadDotEnv loads simple KEY=VALUE lines without overriding the process
// environment. A missing file is intentionally ignored.
func LoadDotEnv(path string) error {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return fmt.Errorf("dòng .env không hợp lệ: %q", line)
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if _, exists := os.LookupEnv(key); !exists {
			if err := os.Setenv(key, value); err != nil {
				return err
			}
		}
	}
	return scanner.Err()
}

func (c Config) Validate() error {
	var errs []error
	if c.DatabaseURL == "" {
		errs = append(errs, errors.New("DATABASE_URL không được để trống"))
	}
	if !strings.HasPrefix(c.StartURL, "https://tangthuvien.org/truyen") {
		errs = append(errs, errors.New("START_URL phải là trang /truyen trên https://tangthuvien.org"))
	}
	if c.CatalogMaxPage < 1 || c.CatalogMaxPage > 10000 {
		errs = append(errs, errors.New("CATALOG_MAX_PAGE phải nằm trong khoảng 1..10000"))
	}
	// A global floor protects the origin even when multiple workers are used.
	if c.RequestInterval < time.Second {
		errs = append(errs, errors.New("REQUEST_INTERVAL phải từ 1s trở lên"))
	}
	if c.RequestJitter < 0 {
		errs = append(errs, errors.New("REQUEST_JITTER không được âm"))
	}
	if c.Workers < 1 || c.Workers > 8 {
		errs = append(errs, errors.New("WORKERS phải nằm trong khoảng 1..8"))
	}
	if c.HTTPTimeout < time.Second {
		errs = append(errs, errors.New("HTTP_TIMEOUT phải từ 1s trở lên"))
	}
	if c.HTTPRetries < 1 || c.HTTPRetries > 5 {
		errs = append(errs, errors.New("HTTP_RETRIES phải nằm trong khoảng 1..5"))
	}
	if c.MaxResponseBytes < 1024 {
		errs = append(errs, errors.New("MAX_RESPONSE_BYTES quá nhỏ"))
	}
	if c.MaxJobAttempts < 1 {
		errs = append(errs, errors.New("MAX_JOB_ATTEMPTS phải lớn hơn 0"))
	}
	return errors.Join(errs...)
}

func env(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return strings.TrimSpace(value)
	}
	return fallback
}

func durationEnv(key string, fallback time.Duration) time.Duration {
	value, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback
	}
	d, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil {
		panic(fmt.Sprintf("%s không hợp lệ: %v", key, err))
	}
	return d
}

func intEnv(key string, fallback int) int {
	value, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback
	}
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		panic(fmt.Sprintf("%s không hợp lệ: %v", key, err))
	}
	return n
}

func int64Env(key string, fallback int64) int64 {
	value, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback
	}
	n, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil {
		panic(fmt.Sprintf("%s không hợp lệ: %v", key, err))
	}
	return n
}

func boolEnv(key string, fallback bool) bool {
	value, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback
	}
	b, err := strconv.ParseBool(strings.TrimSpace(value))
	if err != nil {
		panic(fmt.Sprintf("%s không hợp lệ: %v", key, err))
	}
	return b
}
