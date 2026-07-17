package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/familstorm/crawler-truyen-ttv/internal/model"
	"github.com/familstorm/crawler-truyen-ttv/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	pool *pgxpool.Pool
}

func Open(ctx context.Context, databaseURL string) (*Store, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("DATABASE_URL: %w", err)
	}
	config.MaxConns = 8
	config.MinConns = 1
	config.MaxConnIdleTime = 5 * time.Minute
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("mở PostgreSQL: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("kết nối PostgreSQL: %w", err)
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() {
	s.pool.Close()
}

func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.pool.Exec(ctx, `
        CREATE TABLE IF NOT EXISTS schema_migrations (
            version text PRIMARY KEY,
            applied_at timestamptz NOT NULL DEFAULT now()
        )`); err != nil {
		return fmt.Errorf("tạo schema_migrations: %w", err)
	}

	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		return fmt.Errorf("đọc migrations: %w", err)
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && len(entry.Name()) > 4 && entry.Name()[len(entry.Name())-4:] == ".sql" {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		var applied bool
		err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version=$1)`, name).Scan(&applied)
		if err != nil {
			return fmt.Errorf("kiểm tra migration %s: %w", name, err)
		}
		if applied {
			continue
		}
		body, err := migrations.FS.ReadFile(name)
		if err != nil {
			return fmt.Errorf("đọc migration %s: %w", name, err)
		}
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, string(body)); err == nil {
			_, err = tx.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES ($1)`, name)
		}
		if err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("chạy migration %s: %w", name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit migration %s: %w", name, err)
		}
	}
	return nil
}

func (s *Store) Enqueue(ctx context.Context, kind model.JobKind, target string, priority, maxAttempts int, payload any) error {
	data := []byte(`{}`)
	if payload != nil {
		var err error
		data, err = json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("mã hoá payload: %w", err)
		}
	}
	_, err := s.pool.Exec(ctx, `
        INSERT INTO crawl_jobs(kind, url, priority, max_attempts, payload)
        VALUES ($1, $2, $3, $4, $5::jsonb)
        ON CONFLICT (url) DO UPDATE SET
            priority = GREATEST(crawl_jobs.priority, EXCLUDED.priority),
            max_attempts = GREATEST(crawl_jobs.max_attempts, EXCLUDED.max_attempts),
            payload = crawl_jobs.payload || EXCLUDED.payload,
            updated_at = now()`, kind, target, priority, maxAttempts, data)
	if err != nil {
		return fmt.Errorf("thêm job %s: %w", target, err)
	}
	return nil
}

func (s *Store) EnqueueCatalogPages(ctx context.Context, urls []string, priority, maxAttempts int) error {
	if len(urls) == 0 {
		return nil
	}
	_, err := s.pool.Exec(ctx, `
        INSERT INTO crawl_jobs(kind, url, priority, max_attempts, payload)
        SELECT 'catalog', target_url, $2, $3, '{}'::jsonb
        FROM unnest($1::text[]) AS target_url
        ON CONFLICT (url) DO UPDATE SET
            priority=GREATEST(crawl_jobs.priority, EXCLUDED.priority),
            max_attempts=GREATEST(crawl_jobs.max_attempts, EXCLUDED.max_attempts),
            updated_at=now()`, urls, priority, maxAttempts)
	if err != nil {
		return fmt.Errorf("seed các trang danh mục: %w", err)
	}
	return nil
}

func (s *Store) Claim(ctx context.Context, lease time.Duration) (*model.Job, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
        UPDATE crawl_jobs
        SET status='pending', lease_until=NULL, next_attempt_at=now(),
            last_error=concat_ws(E'\n', last_error, 'lease hết hạn; đưa lại vào queue'), updated_at=now()
        WHERE status='processing' AND lease_until < now()`); err != nil {
		return nil, fmt.Errorf("khôi phục lease: %w", err)
	}

	job := &model.Job{}
	err = tx.QueryRow(ctx, `
        SELECT id, kind, url, priority, attempts, max_attempts, payload
        FROM crawl_jobs
        WHERE status='pending' AND next_attempt_at <= now()
          AND (
              kind='catalog'
              OR (
                  kind='story'
                  AND NOT EXISTS (
                      SELECT 1 FROM crawl_jobs phase
                      WHERE phase.kind='catalog'
                        AND phase.status IN ('pending', 'processing')
                  )
              )
              OR (
                  kind='chapter'
                  AND NOT EXISTS (
                      SELECT 1 FROM crawl_jobs phase
                      WHERE phase.kind IN ('catalog', 'story')
                        AND phase.status IN ('pending', 'processing')
                  )
              )
          )
        ORDER BY priority DESC, id ASC
        FOR UPDATE SKIP LOCKED
        LIMIT 1`).Scan(&job.ID, &job.Kind, &job.URL, &job.Priority, &job.Attempts, &job.MaxAttempts, &job.Payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lấy job: %w", err)
	}
	job.Attempts++
	if _, err := tx.Exec(ctx, `
        UPDATE crawl_jobs
        SET status='processing', attempts=$2, lease_until=now()+$3::interval,
            updated_at=now(), last_error=NULL
        WHERE id=$1`, job.ID, job.Attempts, pgInterval(lease)); err != nil {
		return nil, fmt.Errorf("claim job: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return job, nil
}

func (s *Store) Complete(ctx context.Context, id int64, status int) error {
	_, err := s.pool.Exec(ctx, `
        UPDATE crawl_jobs
        SET status='completed', lease_until=NULL, finished_at=now(), updated_at=now(),
            http_status=$2, last_error=NULL
        WHERE id=$1`, id, nullableStatus(status))
	return err
}

func (s *Store) Release(ctx context.Context, job *model.Job, reason string) error {
	_, err := s.pool.Exec(ctx, `
        UPDATE crawl_jobs
        SET status='pending', attempts=GREATEST(attempts-1, 0), lease_until=NULL,
            next_attempt_at=now(), last_error=$2, updated_at=now()
        WHERE id=$1 AND status='processing'`, job.ID, reason)
	if err != nil {
		return fmt.Errorf("trả job %d về queue: %w", job.ID, err)
	}
	return nil
}

func (s *Store) Fail(ctx context.Context, job *model.Job, jobErr error, status int) error {
	failed := job.Attempts >= job.MaxAttempts || permanentHTTPStatus(status)
	state := "pending"
	if failed {
		state = "failed"
	}
	backoff := time.Duration(1<<min(job.Attempts, 8)) * time.Minute
	_, err := s.pool.Exec(ctx, `
        UPDATE crawl_jobs
        SET status=$2, lease_until=NULL, last_error=$3, http_status=$4,
            next_attempt_at=CASE WHEN $2='failed' THEN next_attempt_at ELSE now()+$5::interval END,
            finished_at=CASE WHEN $2='failed' THEN now() ELSE NULL END,
            updated_at=now()
        WHERE id=$1`, job.ID, state, truncateError(jobErr), nullableStatus(status), pgInterval(backoff))
	return err
}

func (s *Store) SaveCatalogStories(ctx context.Context, stories []model.CatalogStory) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, story := range stories {
		_, err := tx.Exec(ctx, `
            INSERT INTO stories(source_url, source_slug, title, normalized_title, summary, cover_url, rating)
            VALUES ($1,$2,$3,$4,$5,$6,$7)
            ON CONFLICT (source_url) DO UPDATE SET
                title=EXCLUDED.title,
                normalized_title=EXCLUDED.normalized_title,
                summary=CASE WHEN stories.crawled_at IS NULL THEN EXCLUDED.summary ELSE stories.summary END,
                cover_url=COALESCE(NULLIF(stories.cover_url,''), EXCLUDED.cover_url),
                rating=COALESCE(EXCLUDED.rating, stories.rating),
                updated_at=now()`,
			story.URL, story.Slug, story.Title, story.NormalizedTitle, story.Summary, story.CoverURL, story.Rating)
		if err != nil {
			return fmt.Errorf("lưu truyện %s: %w", story.URL, err)
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) SaveStory(ctx context.Context, story model.Story) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var authorID *int64
	if story.Author != "" {
		var id int64
		err := tx.QueryRow(ctx, `
            INSERT INTO authors(name, normalized_name) VALUES ($1,$2)
            ON CONFLICT (normalized_name) DO UPDATE SET name=EXCLUDED.name, updated_at=now()
            RETURNING id`, story.Author, story.NormalizedAuthor).Scan(&id)
		if err != nil {
			return fmt.Errorf("lưu tác giả: %w", err)
		}
		authorID = &id
	}

	var storyID int64
	err = tx.QueryRow(ctx, `
        INSERT INTO stories(
            source_url, source_slug, title, normalized_title, author_id, summary, cover_url,
            status, rating, rating_count, view_count, follower_count, expected_chapter_count,
            source_created_at, crawled_at)
        VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,now())
        ON CONFLICT (source_url) DO UPDATE SET
            source_slug=EXCLUDED.source_slug, title=EXCLUDED.title,
            normalized_title=EXCLUDED.normalized_title, author_id=EXCLUDED.author_id,
            summary=EXCLUDED.summary, cover_url=EXCLUDED.cover_url, status=EXCLUDED.status,
            rating=EXCLUDED.rating, rating_count=EXCLUDED.rating_count,
            view_count=EXCLUDED.view_count, follower_count=EXCLUDED.follower_count,
            expected_chapter_count=EXCLUDED.expected_chapter_count,
            source_created_at=EXCLUDED.source_created_at, crawled_at=now(), updated_at=now()
        RETURNING id`, story.URL, story.Slug, story.Title, story.NormalizedTitle, authorID,
		story.Summary, story.CoverURL, story.Status, story.Rating, story.RatingCount,
		story.ViewCount, story.FollowerCount, story.ExpectedChapterCount, story.SourceCreatedAt).Scan(&storyID)
	if err != nil {
		return fmt.Errorf("lưu chi tiết truyện: %w", err)
	}

	if _, err := tx.Exec(ctx, `DELETE FROM story_genres WHERE story_id=$1`, storyID); err != nil {
		return err
	}
	for _, genre := range story.Genres {
		var genreID int64
		if err := tx.QueryRow(ctx, `
            INSERT INTO genres(name, slug) VALUES ($1,$2)
            ON CONFLICT (slug) DO UPDATE SET name=EXCLUDED.name, updated_at=now()
            RETURNING id`, genre.Name, genre.Slug).Scan(&genreID); err != nil {
			return fmt.Errorf("lưu thể loại %s: %w", genre.Name, err)
		}
		if _, err := tx.Exec(ctx, `
            INSERT INTO story_genres(story_id, genre_id) VALUES ($1,$2)
            ON CONFLICT DO NOTHING`, storyID, genreID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) EnqueueChapters(ctx context.Context, storyURL string, count, maxAttempts int) error {
	if count <= 0 {
		return nil
	}
	_, err := s.pool.Exec(ctx, `
        INSERT INTO crawl_jobs(kind, url, priority, max_attempts, payload)
        SELECT 'chapter', rtrim($1, '/') || '/' || chapter_no, 50, $3,
               jsonb_build_object('chapter_number', chapter_no)
        FROM generate_series(1, $2) AS chapter_no
        ON CONFLICT (url) DO UPDATE SET
            priority=GREATEST(crawl_jobs.priority, EXCLUDED.priority),
            max_attempts=GREATEST(crawl_jobs.max_attempts, EXCLUDED.max_attempts),
            payload=crawl_jobs.payload || EXCLUDED.payload,
            updated_at=now()`, storyURL, count, maxAttempts)
	if err != nil {
		return fmt.Errorf("tạo job chương cho %s: %w", storyURL, err)
	}
	return nil
}

func (s *Store) SaveChapter(ctx context.Context, chapter model.Chapter) error {
	command, err := s.pool.Exec(ctx, `
        INSERT INTO chapters(story_id, source_url, chapter_number, title, content, content_hash)
        SELECT s.id, $2, $3, $4, $5, $6
        FROM stories s WHERE s.source_slug=$1
        ON CONFLICT (story_id, chapter_number) DO UPDATE SET
            source_url=EXCLUDED.source_url, title=EXCLUDED.title, content=EXCLUDED.content,
            content_hash=EXCLUDED.content_hash, crawled_at=now(), updated_at=now()`,
		chapter.StorySlug, chapter.URL, chapter.Number, chapter.Title, chapter.Content, chapter.Hash)
	if err != nil {
		return fmt.Errorf("lưu chương %s: %w", chapter.URL, err)
	}
	if command.RowsAffected() == 0 {
		return fmt.Errorf("không tìm thấy truyện slug=%s", chapter.StorySlug)
	}
	return nil
}

func (s *Store) SaveSourceDocument(ctx context.Context, target, etag, lastModified, hash string, status int) error {
	_, err := s.pool.Exec(ctx, `
        INSERT INTO source_documents(url, etag, last_modified, content_hash, http_status)
        VALUES ($1,$2,$3,$4,$5)
        ON CONFLICT (url) DO UPDATE SET
            etag=EXCLUDED.etag, last_modified=EXCLUDED.last_modified,
            content_hash=EXCLUDED.content_hash, http_status=EXCLUDED.http_status,
            fetched_at=now(), updated_at=now()`, target, nullIfEmpty(etag), nullIfEmpty(lastModified), hash, status)
	return err
}

func (s *Store) Stats(ctx context.Context) (model.QueueStats, error) {
	var stats model.QueueStats
	err := s.pool.QueryRow(ctx, `
        SELECT
            count(*) FILTER (WHERE status='pending'),
            count(*) FILTER (WHERE status='processing'),
            count(*) FILTER (WHERE status='completed'),
            count(*) FILTER (WHERE status='failed')
        FROM crawl_jobs`).Scan(&stats.Pending, &stats.Processing, &stats.Completed, &stats.Failed)
	if err != nil {
		return stats, err
	}
	err = s.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM stories), (SELECT count(*) FROM chapters)`).Scan(&stats.Stories, &stats.Chapters)
	return stats, err
}

func (s *Store) AdminOverview(ctx context.Context) (model.AdminOverview, error) {
	stats, err := s.Stats(ctx)
	if err != nil {
		return model.AdminOverview{}, err
	}
	overview := model.AdminOverview{Queue: stats}
	err = s.pool.QueryRow(ctx, `
        SELECT
            count(*) FILTER (WHERE kind='catalog' AND status='pending'),
            count(*) FILTER (WHERE kind='catalog' AND status='completed'),
            count(*) FILTER (WHERE kind='story' AND status='pending'),
            count(*) FILTER (WHERE kind='story' AND status='completed'),
            count(*) FILTER (WHERE kind='chapter' AND status='pending'),
            count(*) FILTER (WHERE kind='chapter' AND status='completed')
        FROM crawl_jobs`).Scan(
		&overview.CatalogPending, &overview.CatalogComplete,
		&overview.StoryPending, &overview.StoryComplete,
		&overview.ChapterPending, &overview.ChapterComplete)
	return overview, err
}

func (s *Store) AdminStories(ctx context.Context, search string, limit, offset int) ([]model.AdminStory, int64, error) {
	search = strings.TrimSpace(search)
	if limit < 1 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	var total int64
	if err := s.pool.QueryRow(ctx, `
        SELECT count(*) FROM stories
        WHERE ($1 = '' OR title ILIKE '%' || $1 || '%' OR source_slug ILIKE '%' || $1 || '%')`, search).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.pool.Query(ctx, `
        SELECT s.id, s.title, s.source_slug, COALESCE(a.name, ''), s.status,
               s.cover_url, s.expected_chapter_count, count(c.id)::int,
               CASE WHEN s.expected_chapter_count=0 THEN 0
                    ELSE count(c.id)::float8 * 100 / s.expected_chapter_count END,
               s.updated_at
        FROM stories s
        LEFT JOIN authors a ON a.id=s.author_id
        LEFT JOIN chapters c ON c.story_id=s.id
        WHERE ($1 = '' OR s.title ILIKE '%' || $1 || '%' OR s.source_slug ILIKE '%' || $1 || '%')
        GROUP BY s.id, a.name
        ORDER BY s.updated_at DESC, s.id DESC
        LIMIT $2 OFFSET $3`, search, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	stories := make([]model.AdminStory, 0, limit)
	for rows.Next() {
		var story model.AdminStory
		if err := rows.Scan(&story.ID, &story.Title, &story.Slug, &story.Author, &story.Status,
			&story.CoverURL, &story.ExpectedChapter, &story.Downloaded, &story.Progress, &story.UpdatedAt); err != nil {
			return nil, 0, err
		}
		stories = append(stories, story)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return stories, total, nil
}

func (s *Store) AdminJobs(ctx context.Context, kind model.JobKind, status string, limit, offset int) ([]model.AdminJob, int64, model.AdminQueueStats, error) {
	if limit < 1 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	var total int64
	if err := s.pool.QueryRow(ctx, `
        SELECT count(*) FROM crawl_jobs
        WHERE kind=$1 AND ($2='' OR status=$2)`, kind, status).Scan(&total); err != nil {
		return nil, 0, model.AdminQueueStats{}, err
	}
	var stats model.AdminQueueStats
	if err := s.pool.QueryRow(ctx, `
        SELECT
            count(*) FILTER (WHERE status='pending'),
            count(*) FILTER (WHERE status='processing'),
            count(*) FILTER (WHERE status='completed'),
            count(*) FILTER (WHERE status='failed')
        FROM crawl_jobs WHERE kind=$1`, kind).Scan(&stats.Pending, &stats.Processing, &stats.Completed, &stats.Failed); err != nil {
		return nil, 0, stats, err
	}
	rows, err := s.pool.Query(ctx, `
        SELECT id, url, status, priority, attempts, max_attempts,
               next_attempt_at, COALESCE(last_error, ''), updated_at
        FROM crawl_jobs
        WHERE kind=$1 AND ($2='' OR status=$2)
        ORDER BY CASE status WHEN 'processing' THEN 0 WHEN 'pending' THEN 1 WHEN 'failed' THEN 2 ELSE 3 END,
                 updated_at DESC, id DESC
        LIMIT $3 OFFSET $4`, kind, status, limit, offset)
	if err != nil {
		return nil, 0, stats, err
	}
	defer rows.Close()
	jobs := make([]model.AdminJob, 0, limit)
	for rows.Next() {
		var job model.AdminJob
		if err := rows.Scan(&job.ID, &job.URL, &job.Status, &job.Priority, &job.Attempts,
			&job.MaxAttempts, &job.NextAttemptAt, &job.LastError, &job.UpdatedAt); err != nil {
			return nil, 0, stats, err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, stats, err
	}
	return jobs, total, stats, nil
}

func (s *Store) RetryFailed(ctx context.Context) (int64, error) {
	command, err := s.pool.Exec(ctx, `
        UPDATE crawl_jobs SET status='pending', attempts=0, next_attempt_at=now(),
            lease_until=NULL, last_error=NULL, finished_at=NULL, updated_at=now()
        WHERE status='failed'`)
	if err != nil {
		return 0, err
	}
	return command.RowsAffected(), nil
}

func pgInterval(d time.Duration) string {
	return fmt.Sprintf("%f seconds", d.Seconds())
}

func nullableStatus(status int) any {
	if status == 0 {
		return nil
	}
	return status
}

func permanentHTTPStatus(status int) bool {
	if status < 400 || status >= 500 {
		return false
	}
	switch status {
	case 408, 425, 429:
		return false
	default:
		return true
	}
}

func nullIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func truncateError(err error) string {
	if err == nil {
		return ""
	}
	value := err.Error()
	if len(value) > 4000 {
		return value[:4000]
	}
	return value
}
