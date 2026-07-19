-- Claim only from the active phase without scanning pending jobs belonging to
-- later phases. This is essential once the chapter queue reaches millions.
CREATE INDEX IF NOT EXISTS crawl_jobs_pending_kind_priority_idx
    ON crawl_jobs (kind, priority DESC, id)
    INCLUDE (next_attempt_at)
    WHERE status = 'pending';
