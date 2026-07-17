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

type AdminOverview struct {
	Queue           QueueStats
	CatalogPending  int64
	CatalogComplete int64
	StoryPending    int64
	StoryComplete   int64
	ChapterPending  int64
	ChapterComplete int64
}

type AdminStory struct {
	ID              int64
	Title           string
	Slug            string
	Author          string
	Status          string
	CoverURL        string
	Rating          float64
	RatingCount     int
	ExpectedChapter int
	Downloaded      int
	Progress        float64
	UpdatedAt       time.Time
}

type AdminStoryDetail struct {
	ID              int64     `json:"id"`
	Title           string    `json:"title"`
	Slug            string    `json:"slug"`
	SourceURL       string    `json:"source_url"`
	Author          string    `json:"author"`
	Summary         string    `json:"summary"`
	Status          string    `json:"status"`
	CoverURL        string    `json:"cover_url"`
	Genres          []string  `json:"genres"`
	Rating          float64   `json:"rating"`
	RatingCount     int       `json:"rating_count"`
	ViewCount       int64     `json:"view_count"`
	FollowerCount   int64     `json:"follower_count"`
	ExpectedChapter int       `json:"expected_chapters"`
	Downloaded      int       `json:"downloaded_chapters"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type AdminJob struct {
	ID            int64
	URL           string
	Status        string
	Priority      int
	Attempts      int
	MaxAttempts   int
	NextAttemptAt time.Time
	LastError     string
	UpdatedAt     time.Time
}

type AdminQueueStats struct {
	Pending    int64
	Processing int64
	Completed  int64
	Failed     int64
}
