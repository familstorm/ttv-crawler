package fetcher

import "context"

// PageFetcher is the source boundary used by the crawler. It can be backed by
// a normal HTTP client or by a real browser session.
type PageFetcher interface {
	Fetch(context.Context, string) (Result, error)
}
