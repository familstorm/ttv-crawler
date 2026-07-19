package fetcher

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

type Result struct {
	Body         []byte
	Status       int
	ETag         string
	LastModified string
}

type HTTPError struct {
	URL    string
	Status int
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("HTTP %d từ %s", e.Status, e.URL)
}

type Config struct {
	Interval          time.Duration
	Jitter            time.Duration
	Timeout           time.Duration
	Retries           int
	MaxResponseBytes  int64
	UserAgent         string
	BrowserExecutable string
}

type Fetcher struct {
	browserCtx       context.Context
	cancelBrowser    context.CancelFunc
	cancelAllocator  context.CancelFunc
	browserMu        sync.Mutex
	limiter          *limiter
	retries          int
	timeout          time.Duration
	maxResponseBytes int64
	userAgent        string
	logger           *slog.Logger
}

type navigationState struct {
	mu           sync.Mutex
	status       int
	mimeType     string
	etag         string
	lastModified string
	retryAfter   string
	responseURL  string
}

var blockedResourcePatterns = []*network.BlockPattern{
	{URLPattern: "*://*:*/*.css", Block: true},
	{URLPattern: "*://*:*/*.js", Block: true},
	{URLPattern: "*://*:*/*.png", Block: true},
	{URLPattern: "*://*:*/*.jpg", Block: true},
	{URLPattern: "*://*:*/*.jpeg", Block: true},
	{URLPattern: "*://*:*/*.webp", Block: true},
	{URLPattern: "*://*:*/*.gif", Block: true},
	{URLPattern: "*://*:*/*.svg", Block: true},
	{URLPattern: "*://*:*/*.woff", Block: true},
	{URLPattern: "*://*:*/*.woff2", Block: true},
	{URLPattern: "*://*:*/*.ttf", Block: true},
	{URLPattern: "*://*:*/*.mp3", Block: true},
	{URLPattern: "*://*:*/*.mp4", Block: true},
	{URLPattern: "*://*:*/_next/static/*", Block: true},
}

func New(ctx context.Context, cfg Config, logger *slog.Logger) (*Fetcher, error) {
	browserPath, err := findBrowser(cfg.BrowserExecutable)
	if err != nil {
		return nil, err
	}
	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	opts = append(opts,
		chromedp.ExecPath(browserPath),
		chromedp.Headless,
		chromedp.NoSandbox,
		chromedp.DisableGPU,
		chromedp.UserAgent(cfg.UserAgent),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("disable-background-networking", true),
		chromedp.Flag("disable-component-update", true),
		chromedp.Flag("disable-default-apps", true),
		chromedp.Flag("disable-extensions", true),
		chromedp.Flag("disable-sync", true),
		chromedp.Flag("metrics-recording-only", true),
		chromedp.Flag("no-first-run", true),
	)
	allocatorCtx, cancelAllocator := chromedp.NewExecAllocator(ctx, opts...)
	browserCtx, cancelBrowser := chromedp.NewContext(allocatorCtx)
	// The first Run allocates the browser. It must use the long-lived browser
	// context: chromedp documents that timing out this first Run stops the
	// entire browser, which previously made every subsequent request fail with
	// "context canceled".
	if err := chromedp.Run(browserCtx); err != nil {
		cancelBrowser()
		cancelAllocator()
		return nil, fmt.Errorf("khởi động Chromium %s: %w", browserPath, err)
	}
	logger.Info("Chromium transport sẵn sàng", "executable", browserPath)
	return &Fetcher{
		browserCtx:       browserCtx,
		cancelBrowser:    cancelBrowser,
		cancelAllocator:  cancelAllocator,
		limiter:          newLimiter(cfg.Interval, cfg.Jitter),
		retries:          cfg.Retries,
		timeout:          cfg.Timeout,
		maxResponseBytes: cfg.MaxResponseBytes,
		userAgent:        cfg.UserAgent,
		logger:           logger,
	}, nil
}

func (f *Fetcher) Close() {
	if f.cancelBrowser != nil {
		f.cancelBrowser()
	}
	if f.cancelAllocator != nil {
		f.cancelAllocator()
	}
}

func (f *Fetcher) Fetch(ctx context.Context, target string) (Result, error) {
	parsed, err := url.Parse(target)
	if err != nil {
		return Result{}, fmt.Errorf("URL không hợp lệ: %w", err)
	}
	if parsed.Scheme != "https" || parsed.Hostname() != "tangthuvien.org" {
		return Result{}, fmt.Errorf("từ chối crawl ngoài tangthuvien.org: %s", target)
	}
	return f.fetchWithRetry(ctx, target)
}

func (f *Fetcher) fetchWithRetry(ctx context.Context, target string) (Result, error) {
	return f.fetchWithRetryUsing(ctx, target, f.fetchOnce)
}

func (f *Fetcher) fetchWithRetryUsing(ctx context.Context, target string, fetchAttempt func(context.Context, string) (Result, time.Duration, error)) (Result, error) {
	var lastErr error
	for attempt := 1; attempt <= f.retries; attempt++ {
		if err := f.limiter.Wait(ctx); err != nil {
			return Result{}, err
		}
		result, retryAfter, err := fetchAttempt(ctx, target)
		if err == nil {
			return result, nil
		}
		// A caller cancellation means the worker/container is shutting down. Do
		// not report it as a browser failure or start another retry; the runner
		// will release the claimed job and restore its attempt count.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Result{}, ctxErr
		}
		lastErr = err
		if attempt == f.retries || !retryable(err) {
			break
		}
		backoff := time.Duration(1<<(attempt-1)) * 2 * time.Second
		if retryAfter > backoff {
			backoff = retryAfter
		}
		f.logger.Warn("browser request lỗi, sẽ thử lại", "url", target, "attempt", attempt, "wait", backoff, "error", err)
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return Result{}, ctx.Err()
		case <-timer.C:
		}
	}
	return Result{}, lastErr
}

func (f *Fetcher) fetchOnce(ctx context.Context, target string) (Result, time.Duration, error) {
	// Serialize navigation even when more queue workers are enabled. This keeps a
	// single browser session from issuing overlapping document requests.
	f.browserMu.Lock()
	defer f.browserMu.Unlock()

	// A failed navigation can leave a tab waiting on a broken connection.
	// Create and close a tab per attempt so retries always start from a clean
	// target while reusing the already-running Chromium process.
	tabCtx, cancelTab := chromedp.NewContext(f.browserCtx)
	defer cancelTab()
	runCtx, cancelRun := context.WithTimeout(tabCtx, f.timeout)
	defer cancelRun()
	stopOnJobCancel := context.AfterFunc(ctx, cancelRun)
	defer stopOnJobCancel()

	state := &navigationState{}
	chromedp.ListenTarget(runCtx, func(event any) {
		response, ok := event.(*network.EventResponseReceived)
		if !ok || response.Type != network.ResourceTypeDocument || response.Response == nil {
			return
		}
		state.mu.Lock()
		defer state.mu.Unlock()
		state.status = int(response.Response.Status)
		state.mimeType = response.Response.MimeType
		state.etag = responseHeader(response.Response.Headers, "etag")
		state.lastModified = responseHeader(response.Response.Headers, "last-modified")
		state.retryAfter = responseHeader(response.Response.Headers, "retry-after")
		state.responseURL = response.Response.URL
	})

	var body string
	var finalURL string
	actions := chromedp.Tasks{
		network.Enable(),
		network.SetBlockedURLs().WithURLPatterns(blockedResourcePatterns),
		// chromedp.Navigate waits for the browser's load event. A page can
		// keep subresources open indefinitely, so that behavior turns a usable
		// document into a timeout. Issue the CDP navigation command directly,
		// then wait only for the DOM we need to parse.
		chromedp.ActionFunc(func(actionCtx context.Context) error {
			_, _, errorText, _, err := page.Navigate(target).Do(actionCtx)
			if err != nil {
				return err
			}
			if errorText != "" {
				return fmt.Errorf("page load error %s", errorText)
			}
			return nil
		}),
		chromedp.WaitReady("body", chromedp.ByQuery),
		page.StopLoading(),
		chromedp.Location(&finalURL),
		chromedp.Evaluate(`document.contentType === "text/plain" ? document.body.innerText : document.documentElement.outerHTML`, &body),
	}
	if err := chromedp.Run(runCtx, actions); err != nil {
		return Result{}, 0, fmt.Errorf("Chromium điều hướng %s: %w", target, err)
	}
	final, err := url.Parse(finalURL)
	if err != nil || final.Scheme != "https" || final.Hostname() != "tangthuvien.org" {
		return Result{}, 0, fmt.Errorf("từ chối redirect ngoài tangthuvien.org: %s", finalURL)
	}

	state.mu.Lock()
	status := state.status
	etag := state.etag
	lastModified := state.lastModified
	retryAfterValue := state.retryAfter
	responseURL := state.responseURL
	state.mu.Unlock()
	if status == 0 {
		return Result{}, 0, fmt.Errorf("Chromium không ghi nhận HTTP response cho %s", target)
	}
	if responseURL != "" {
		responseParsed, parseErr := url.Parse(responseURL)
		if parseErr != nil || responseParsed.Hostname() != "tangthuvien.org" {
			return Result{}, 0, fmt.Errorf("response document ngoài tangthuvien.org: %s", responseURL)
		}
	}
	retryAfter := parseRetryAfter(retryAfterValue)
	if retryAfter > 0 {
		f.limiter.BlockFor(retryAfter)
	}
	if status < 200 || status >= 300 {
		return Result{}, retryAfter, &HTTPError{URL: target, Status: status}
	}
	if int64(len(body)) > f.maxResponseBytes {
		return Result{}, 0, fmt.Errorf("response vượt quá giới hạn %d bytes", f.maxResponseBytes)
	}
	return Result{
		Body:         []byte(body),
		Status:       status,
		ETag:         etag,
		LastModified: lastModified,
	}, 0, nil
}

func retryable(err error) bool {
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		return true
	}
	switch httpErr.Status {
	case http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusInternalServerError,
		http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func responseHeader(headers network.Headers, key string) string {
	for name, value := range headers {
		if strings.EqualFold(name, key) {
			return fmt.Sprint(value)
		}
	}
	return ""
}

func parseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil {
		if wait := time.Until(at); wait > 0 {
			return wait
		}
	}
	return 0
}

func findBrowser(configured string) (string, error) {
	if configured != "" {
		path, err := exec.LookPath(configured)
		if err != nil {
			return "", fmt.Errorf("không tìm thấy BROWSER_EXECUTABLE=%s: %w", configured, err)
		}
		return path, nil
	}
	for _, candidate := range []string{"chromium-browser", "chromium", "google-chrome", "google-chrome-stable"} {
		if path, err := exec.LookPath(candidate); err == nil {
			return path, nil
		}
	}
	return "", errors.New("không tìm thấy Chromium/Chrome; đặt BROWSER_EXECUTABLE")
}

type limiter struct {
	mu       sync.Mutex
	next     time.Time
	interval time.Duration
	jitter   time.Duration
	random   *rand.Rand
}

// Limiter is exported for browser-backed fetching so every transport can share
// the same politeness guarantees.
type Limiter = limiter

func NewLimiter(interval, jitter time.Duration) *Limiter {
	return newLimiter(interval, jitter)
}

func newLimiter(interval, jitter time.Duration) *limiter {
	return &limiter{
		interval: interval,
		jitter:   jitter,
		random:   rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

func (l *limiter) Wait(ctx context.Context) error {
	l.mu.Lock()
	now := time.Now()
	start := now
	if l.next.After(start) {
		start = l.next
	}
	extra := time.Duration(0)
	if l.jitter > 0 {
		extra = time.Duration(l.random.Int63n(int64(l.jitter) + 1))
	}
	l.next = start.Add(l.interval + extra)
	l.mu.Unlock()

	wait := time.Until(start)
	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (l *limiter) BlockFor(wait time.Duration) {
	if wait <= 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	blockedUntil := time.Now().Add(wait)
	if blockedUntil.After(l.next) {
		l.next = blockedUntil
	}
}

func (l *limiter) EnsureInterval(minimum time.Duration) bool {
	if minimum <= 0 {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if minimum <= l.interval {
		return false
	}
	l.interval = minimum
	minimumNext := time.Now().Add(minimum)
	if minimumNext.After(l.next) {
		l.next = minimumNext
	}
	return true
}
