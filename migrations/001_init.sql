CREATE TABLE IF NOT EXISTS schema_migrations (
    version     text PRIMARY KEY,
    applied_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS authors (
    id              bigserial PRIMARY KEY,
    name            text NOT NULL,
    normalized_name text NOT NULL UNIQUE,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS genres (
    id         bigserial PRIMARY KEY,
    name       text NOT NULL,
    slug       text NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS stories (
    id                     bigserial PRIMARY KEY,
    source_url             text NOT NULL UNIQUE,
    source_slug            text NOT NULL UNIQUE,
    title                  text NOT NULL,
    normalized_title       text NOT NULL,
    author_id              bigint REFERENCES authors(id) ON DELETE SET NULL,
    summary                text NOT NULL DEFAULT '',
    cover_url              text NOT NULL DEFAULT '',
    status                 text NOT NULL DEFAULT 'unknown'
                           CHECK (status IN ('updating', 'completed', 'paused', 'unknown')),
    rating                 numeric(3,2),
    rating_count           integer NOT NULL DEFAULT 0 CHECK (rating_count >= 0),
    view_count             bigint NOT NULL DEFAULT 0 CHECK (view_count >= 0),
    follower_count         bigint NOT NULL DEFAULT 0 CHECK (follower_count >= 0),
    expected_chapter_count integer NOT NULL DEFAULT 0 CHECK (expected_chapter_count >= 0),
    source_created_at      date,
    discovered_at          timestamptz NOT NULL DEFAULT now(),
    crawled_at             timestamptz,
    created_at             timestamptz NOT NULL DEFAULT now(),
    updated_at             timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS stories_normalized_title_idx ON stories (normalized_title);
CREATE INDEX IF NOT EXISTS stories_author_id_idx ON stories (author_id);

CREATE TABLE IF NOT EXISTS story_genres (
    story_id bigint NOT NULL REFERENCES stories(id) ON DELETE CASCADE,
    genre_id bigint NOT NULL REFERENCES genres(id) ON DELETE CASCADE,
    PRIMARY KEY (story_id, genre_id)
);

CREATE TABLE IF NOT EXISTS chapters (
    id             bigserial PRIMARY KEY,
    story_id       bigint NOT NULL REFERENCES stories(id) ON DELETE CASCADE,
    source_url     text NOT NULL UNIQUE,
    chapter_number integer NOT NULL CHECK (chapter_number > 0),
    title          text NOT NULL,
    content        text NOT NULL,
    content_hash   text NOT NULL,
    crawled_at     timestamptz NOT NULL DEFAULT now(),
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (story_id, chapter_number)
);

CREATE INDEX IF NOT EXISTS chapters_story_number_idx ON chapters (story_id, chapter_number);

CREATE TABLE IF NOT EXISTS crawl_jobs (
    id              bigserial PRIMARY KEY,
    kind            text NOT NULL CHECK (kind IN ('catalog', 'story', 'chapter')),
    url             text NOT NULL UNIQUE,
    priority        integer NOT NULL DEFAULT 0,
    status          text NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending', 'processing', 'completed', 'failed')),
    attempts        integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    max_attempts    integer NOT NULL DEFAULT 8 CHECK (max_attempts > 0),
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    lease_until     timestamptz,
    last_error      text,
    http_status     integer,
    payload         jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    finished_at     timestamptz
);

CREATE INDEX IF NOT EXISTS crawl_jobs_claim_idx
    ON crawl_jobs (status, next_attempt_at, priority DESC, id)
    WHERE status IN ('pending', 'processing');

CREATE TABLE IF NOT EXISTS source_documents (
    url           text PRIMARY KEY,
    etag          text,
    last_modified text,
    content_hash  text,
    http_status   integer NOT NULL,
    fetched_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE OR REPLACE VIEW story_progress AS
SELECT
    s.id,
    s.title,
    s.source_url,
    s.expected_chapter_count,
    count(c.id)::integer AS downloaded_chapter_count,
    CASE
        WHEN s.expected_chapter_count = 0 THEN 0
        ELSE round(count(c.id)::numeric * 100 / s.expected_chapter_count, 2)
    END AS progress_percent
FROM stories s
LEFT JOIN chapters c ON c.story_id = s.id
GROUP BY s.id;
