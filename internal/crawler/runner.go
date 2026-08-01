package crawler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"sync"
	"time"

	"github.com/familstorm/crawler-truyen-ttv/internal/fetcher"
	"github.com/familstorm/crawler-truyen-ttv/internal/model"
	"github.com/familstorm/crawler-truyen-ttv/internal/parser"
	"github.com/familstorm/crawler-truyen-ttv/internal/store"
)

const (
	catalogPriority = 100
	storyPriority   = 80
	leaseDuration   = 10 * time.Minute
	idlePoll        = 2 * time.Second
)

type Config struct {
	Workers        int
	MaxJobAttempts int
	CatalogMaxPage int
	IdleExitAfter  time.Duration
}

type CoverDownloader interface {
	Download(context.Context, string, string) (string, error)
}

type Runner struct {
	store   *store.Store
	fetcher fetcher.PageFetcher
	cover   CoverDownloader
	config  Config
	logger  *slog.Logger
}

func New(s *store.Store, f fetcher.PageFetcher, cfg Config, logger *slog.Logger, coverDownloaders ...CoverDownloader) *Runner {
	var coverDownloader CoverDownloader
	if len(coverDownloaders) > 0 {
		coverDownloader = coverDownloaders[0]
	}
	return &Runner{store: s, fetcher: f, cover: coverDownloader, config: cfg, logger: logger}
}

func (r *Runner) Seed(ctx context.Context, startURL string) error {
	parsed, err := url.Parse(startURL)
	if err != nil {
		return fmt.Errorf("START_URL không hợp lệ: %w", err)
	}
	urls := make([]string, 0, r.config.CatalogMaxPage)
	for page := 1; page <= r.config.CatalogMaxPage; page++ {
		pageURL := *parsed
		query := pageURL.Query()
		query.Set("page", fmt.Sprintf("%d", page))
		pageURL.RawQuery = query.Encode()
		urls = append(urls, pageURL.String())
	}
	if err := r.store.EnqueueCatalogPages(ctx, urls, catalogPriority, r.config.MaxJobAttempts); err != nil {
		return err
	}
	r.logger.Info("đã seed toàn bộ trang danh mục", "pages", len(urls), "first", urls[0], "last", urls[len(urls)-1])
	return nil
}

func (r *Runner) Run(ctx context.Context) error {
	r.logger.Info("crawler bắt đầu", "workers", r.config.Workers)
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	errCh := make(chan error, r.config.Workers)
	var wg sync.WaitGroup
	for i := 1; i <= r.config.Workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			if err := r.worker(workerCtx, workerID); err != nil && !errors.Is(err, context.Canceled) {
				select {
				case errCh <- err:
				default:
				}
				cancel()
			}
		}(i)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-ctx.Done():
		cancel()
		<-done
		return ctx.Err()
	case err := <-errCh:
		cancel()
		<-done
		return err
	case <-done:
		return nil
	}
}

func (r *Runner) worker(ctx context.Context, workerID int) error {
	var idleSince time.Time
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		job, err := r.store.Claim(ctx, leaseDuration)
		if err != nil {
			return fmt.Errorf("worker %d claim: %w", workerID, err)
		}
		if job == nil {
			if idleSince.IsZero() {
				idleSince = time.Now()
			}
			if r.config.IdleExitAfter > 0 && time.Since(idleSince) >= r.config.IdleExitAfter {
				r.logger.Info("queue rỗng, worker kết thúc", "worker", workerID)
				return nil
			}
			timer := time.NewTimer(idlePoll)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
			continue
		}
		idleSince = time.Time{}
		r.logger.Info("đang xử lý", "worker", workerID, "kind", job.Kind, "url", job.URL, "attempt", job.Attempts)
		status, err := r.process(ctx, job)
		if err == nil {
			if err := r.store.Complete(ctx, job.ID, status); err != nil {
				return fmt.Errorf("đánh dấu hoàn tất job %d: %w", job.ID, err)
			}
			continue
		}
		if ctx.Err() != nil {
			releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			releaseErr := r.store.Release(releaseCtx, job, "worker dừng an toàn; trả job về queue")
			cancel()
			if releaseErr != nil {
				r.logger.Error("không thể trả job về queue khi dừng", "job", job.ID, "error", releaseErr)
			}
			return ctx.Err()
		}
		// A robots.txt denial will not change on a retry, so burn the remaining
		// attempts and let Fail record the job as permanently failed instead of
		// re-requesting a URL the origin asked us not to touch.
		if errors.Is(err, fetcher.ErrRobotsDisallowed) {
			job.Attempts = job.MaxAttempts
		}
		if markErr := r.store.Fail(ctx, job, err, status); markErr != nil {
			return fmt.Errorf("job %d lỗi (%v), đồng thời không lưu được trạng thái: %w", job.ID, err, markErr)
		}
		r.logger.Error("job lỗi", "worker", workerID, "kind", job.Kind, "url", job.URL, "attempt", job.Attempts, "error", err)
	}
}

func (r *Runner) process(ctx context.Context, job *model.Job) (int, error) {
	result, err := r.fetcher.Fetch(ctx, job.URL)
	if err != nil {
		return httpStatus(err), err
	}
	hash := sha256.Sum256(result.Body)
	if err := r.store.SaveSourceDocument(ctx, job.URL, result.ETag, result.LastModified, hex.EncodeToString(hash[:]), result.Status); err != nil {
		return result.Status, fmt.Errorf("lưu dấu vết nguồn: %w", err)
	}

	switch job.Kind {
	case model.JobCatalog:
		page, err := parser.Catalog(bytes.NewReader(result.Body), job.URL)
		if err != nil {
			return result.Status, err
		}
		if err := r.store.SaveCatalogStories(ctx, page.Stories); err != nil {
			return result.Status, err
		}
		for _, story := range page.Stories {
			if err := r.store.Enqueue(ctx, model.JobStory, story.URL, storyPriority, r.config.MaxJobAttempts, nil); err != nil {
				return result.Status, err
			}
		}
		// All catalog pages are seeded canonically up front. Do not follow the
		// pagination URL here because a different query-parameter order would be a
		// distinct queue key and could download the same page twice.
		r.logger.Info("đã đọc trang danh mục", "url", job.URL, "stories", len(page.Stories))

	case model.JobStory:
		story, err := parser.Story(bytes.NewReader(result.Body), job.URL)
		if err != nil {
			return result.Status, err
		}
		if err := r.store.SaveStory(ctx, story); err != nil {
			return result.Status, err
		}
		if r.cover != nil && story.CoverURL != "" {
			localURL, err := r.cover.Download(ctx, story.CoverURL, story.Slug)
			if err != nil {
				return result.Status, fmt.Errorf("tải ảnh bìa %s: %w", story.Title, err)
			}
			if err := r.store.UpdateStoryCover(ctx, story.URL, localURL); err != nil {
				return result.Status, fmt.Errorf("lưu đường dẫn ảnh bìa %s: %w", story.Title, err)
			}
			r.logger.Info("đã lưu ảnh bìa", "title", story.Title, "url", localURL)
		}
		if err := r.store.EnqueueChapters(ctx, story.URL, story.ExpectedChapterCount, r.config.MaxJobAttempts); err != nil {
			return result.Status, err
		}
		r.logger.Info("đã chuẩn hoá truyện", "title", story.Title, "chapters", story.ExpectedChapterCount)

	case model.JobChapter:
		chapter, err := parser.Chapter(bytes.NewReader(result.Body), job.URL)
		if err != nil {
			return result.Status, err
		}
		if err := r.store.SaveChapter(ctx, chapter); err != nil {
			return result.Status, err
		}
		r.logger.Info("đã lưu chương", "story", chapter.StorySlug, "chapter", chapter.Number)

	default:
		return result.Status, fmt.Errorf("loại job không hỗ trợ: %s", job.Kind)
	}
	return result.Status, nil
}

func httpStatus(err error) int {
	var httpErr *fetcher.HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.Status
	}
	return 0
}
