package cover

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/familstorm/crawler-truyen-ttv/internal/fetcher"
)

const defaultMaxBytes int64 = 5 * 1024 * 1024

type Downloader struct {
	directory string
	client    *http.Client
	limiter   *fetcher.Limiter
	userAgent string
	maxBytes  int64
}

type Config struct {
	Directory string
	Timeout   time.Duration
	Interval  time.Duration
	Jitter    time.Duration
	UserAgent string
	MaxBytes  int64
}

func New(cfg Config) (*Downloader, error) {
	if strings.TrimSpace(cfg.Directory) == "" {
		return nil, fmt.Errorf("thư mục cover không được để trống")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.MaxBytes <= 0 {
		cfg.MaxBytes = defaultMaxBytes
	}
	if err := os.MkdirAll(cfg.Directory, 0o755); err != nil {
		return nil, fmt.Errorf("tạo thư mục cover: %w", err)
	}
	return &Downloader{
		directory: cfg.Directory,
		client:    &http.Client{Timeout: cfg.Timeout},
		limiter:   fetcher.NewLimiter(cfg.Interval, cfg.Jitter),
		userAgent: cfg.UserAgent,
		maxBytes:  cfg.MaxBytes,
	}, nil
}

func (d *Downloader) Download(ctx context.Context, sourceURL, slug string) (string, error) {
	parsed, err := url.Parse(sourceURL)
	if err != nil || parsed.Scheme != "https" || !allowedHost(parsed.Hostname()) {
		return "", fmt.Errorf("URL cover không được phép: %s", sourceURL)
	}
	base := safeName(slug)
	if base == "" {
		return "", fmt.Errorf("slug cover không hợp lệ: %q", slug)
	}
	if existing := d.existing(base); existing != "" {
		return "/static/covers/" + filepath.Base(existing), nil
	}
	if err := d.limiter.Wait(ctx); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return "", fmt.Errorf("tạo request cover: %w", err)
	}
	req.Header.Set("Accept", "image/avif,image/webp,image/apng,image/svg+xml,image/*,*/*;q=0.8")
	if d.userAgent != "" {
		req.Header.Set("User-Agent", d.userAgent)
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("tải cover %s: %w", sourceURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("tải cover %s: HTTP %d", sourceURL, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, d.maxBytes+1))
	if err != nil {
		return "", fmt.Errorf("đọc cover %s: %w", sourceURL, err)
	}
	if int64(len(body)) > d.maxBytes {
		return "", fmt.Errorf("cover vượt quá giới hạn %d bytes: %s", d.maxBytes, sourceURL)
	}
	ext := extension(resp.Header.Get("Content-Type"), body)
	if ext == "" {
		return "", fmt.Errorf("response không phải ảnh: %s", sourceURL)
	}
	filename := base + ext
	temporary, err := os.CreateTemp(d.directory, "."+base+"-*")
	if err != nil {
		return "", fmt.Errorf("tạo file tạm cover: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return "", fmt.Errorf("đặt quyền file cover %s: %w", sourceURL, err)
	}
	if _, err := temporary.Write(body); err != nil {
		_ = temporary.Close()
		return "", fmt.Errorf("ghi cover %s: %w", sourceURL, err)
	}
	if err := temporary.Close(); err != nil {
		return "", fmt.Errorf("đóng file cover %s: %w", sourceURL, err)
	}
	destination := filepath.Join(d.directory, filename)
	if err := os.Rename(temporaryName, destination); err != nil {
		return "", fmt.Errorf("lưu cover %s: %w", sourceURL, err)
	}
	return "/static/covers/" + filename, nil
}

func (d *Downloader) existing(base string) string {
	for _, ext := range []string{".jpg", ".jpeg", ".png", ".webp", ".gif", ".avif"} {
		candidate := filepath.Join(d.directory, base+ext)
		if info, err := os.Stat(candidate); err == nil && info.Size() > 0 {
			return candidate
		}
	}
	return ""
}

func allowedHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	return host == "tangthuvien.org" || strings.HasSuffix(host, ".tangthuvien.org") || host == "cdn.truyenonl.net"
}

func safeName(value string) string {
	var builder strings.Builder
	for _, char := range strings.ToLower(value) {
		if unicode.IsLetter(char) || unicode.IsDigit(char) || char == '-' || char == '_' {
			builder.WriteRune(char)
		}
	}
	return strings.Trim(builder.String(), "-_")
}

func extension(contentType string, body []byte) string {
	mime := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	if mime == "" || mime == "application/octet-stream" {
		mime = strings.ToLower(http.DetectContentType(body))
	}
	switch mime {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	case "image/avif":
		return ".avif"
	default:
		return ""
	}
}
