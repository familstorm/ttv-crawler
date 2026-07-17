package model

import "time"

type JobKind string

const (
	JobCatalog JobKind = "catalog"
	JobStory   JobKind = "story"
	JobChapter JobKind = "chapter"
)

type Job struct {
	ID          int64
	Kind        JobKind
	URL         string
	Priority    int
	Attempts    int
	MaxAttempts int
	Payload     []byte
}

type CatalogStory struct {
	URL             string
	Slug            string
	Title           string
	NormalizedTitle string
	Summary         string
	CoverURL        string
	Rating          *float64
}

type CatalogPage struct {
	Stories []CatalogStory
	NextURL string
}

type Genre struct {
	Name string
	Slug string
}

type Story struct {
	URL                  string
	Slug                 string
	Title                string
	NormalizedTitle      string
	Author               string
	NormalizedAuthor     string
	Summary              string
	CoverURL             string
	Status               string
	Rating               *float64
	RatingCount          int
	ViewCount            int64
	FollowerCount        int64
	ExpectedChapterCount int
	SourceCreatedAt      *time.Time
	Genres               []Genre
}

type Chapter struct {
	StorySlug string
	URL       string
	Number    int
	Title     string
	Content   string
	Hash      string
}

type QueueStats struct {
	Pending    int64
	Processing int64
	Completed  int64
	Failed     int64
	Stories    int64
	Chapters   int64
}
