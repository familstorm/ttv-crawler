-- Speed up the phase gates used for every queue claim. Without this partial
-- index, checking whether catalog/story work is still active scans the full
-- multi-million-row chapter queue.
CREATE INDEX IF NOT EXISTS crawl_jobs_active_phase_idx
    ON crawl_jobs (kind, status)
    WHERE status IN ('pending', 'processing');

-- Match the queue's priority/id ordering while leaving next_attempt_at
-- available for the eligibility filter.
CREATE INDEX IF NOT EXISTS crawl_jobs_pending_priority_idx
    ON crawl_jobs (priority DESC, id)
    INCLUDE (kind, next_attempt_at)
    WHERE status = 'pending';

-- Avoid scanning active/pending rows when recovering expired leases.
CREATE INDEX IF NOT EXISTS crawl_jobs_expired_lease_idx
    ON crawl_jobs (lease_until)
    WHERE status = 'processing';
