package parser

import (
	"strings"
	"testing"
)

func TestCatalog(t *testing.T) {
	html := `<!doctype html><html><body><main>
      <div class="grid">
        <div><div class="card">
          <a href="/dau-la-dai-luc-3"><img alt="Đấu La Đại Lục 3" src="/_next/image?url=https%3A%2F%2Fcdn.example%2Fcover.jpg&w=640&q=75"></a>
          <div><a href="/dau-la-dai-luc-3"><h3> Đấu La Đại Lục 3 </h3></a>
          <span><svg class="lucide lucide-star"></svg>5.0</span><p> Mô tả   truyện. </p></div>
        </div></div>
      </div>
      <nav aria-label="Pagination"><a aria-label="Next page" href="?sort=rate&amp;order=desc&amp;page=2">next</a></nav>
    </main></body></html>`

	page, err := Catalog(strings.NewReader(html), "https://tangthuvien.org/truyen?sort=rate&order=desc")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Stories) != 1 {
		t.Fatalf("stories=%d, want 1", len(page.Stories))
	}
	story := page.Stories[0]
	if story.Title != "Đấu La Đại Lục 3" || story.NormalizedTitle != "dau-la-dai-luc-3" {
		t.Fatalf("unexpected title: %#v", story)
	}
	if story.Rating == nil || *story.Rating != 5.0 {
		t.Fatalf("rating=%v, want 5.0", story.Rating)
	}
	if story.CoverURL != "https://cdn.example/cover.jpg" {
		t.Fatalf("cover=%q", story.CoverURL)
	}
	if page.NextURL != "https://tangthuvien.org/truyen?sort=rate&order=desc&page=2" {
		t.Fatalf("next=%q", page.NextURL)
	}
}

func TestStory(t *testing.T) {
	html := `<!doctype html><html><head>
      <title>Lạn Kha Kỳ Duyên - Tác giả ẩn danh | Tàng Thư Viện</title>
      <meta property="og:image" content="https://cdn.example/cover.jpg">
    </head><body><main>
      <section><div><div class="head"><img alt="Lạn Kha Kỳ Duyên" src="https://cdn.example/cover.jpg"><div>
        <h1> Lạn Kha Kỳ Duyên </h1>
        <div><span>Đang cập nhật</span><span>·</span><span>542 Lượt Xem</span></div>
        <div><span>1,076 Chương</span><span>·</span><span>349 Người Theo Dõi</span></div>
        <div><span>5.0 (22 đánh giá)</span></div>
      </div></div><div><a href="/truyen?genre=tien-hiep">Tiên hiệp</a></div></div></section>
      <section><div class="card">
        <div><h2>Tóm Tắt</h2><div><p>Dòng một.</p><p>Dòng hai.</p></div></div>
        <div><span>Ngày Thêm</span><span>3 tháng 12 năm 2022</span></div>
      </div></section>
    </main></body></html>`

	story, err := Story(strings.NewReader(html), "https://tangthuvien.org/lan-kha-ky-duyen")
	if err != nil {
		t.Fatal(err)
	}
	if story.Title != "Lạn Kha Kỳ Duyên" || story.Author != "ẩn danh" {
		t.Fatalf("unexpected metadata: %#v", story)
	}
	if story.Status != "updating" || story.ExpectedChapterCount != 1076 || story.ViewCount != 542 || story.FollowerCount != 349 {
		t.Fatalf("unexpected stats: %#v", story)
	}
	if story.Rating == nil || *story.Rating != 5 || story.RatingCount != 22 {
		t.Fatalf("unexpected rating: %#v", story)
	}
	if story.Summary != "Dòng một.\n\nDòng hai." {
		t.Fatalf("summary=%q", story.Summary)
	}
	if len(story.Genres) != 1 || story.Genres[0].Slug != "tien-hiep" {
		t.Fatalf("genres=%#v", story.Genres)
	}
	if story.SourceCreatedAt == nil || story.SourceCreatedAt.Format("2006-01-02") != "2022-12-03" {
		t.Fatalf("date=%v", story.SourceCreatedAt)
	}
}

func TestChapter(t *testing.T) {
	html := `<!doctype html><html><body><main><div><div></div><div class="content-card">
      <div><h1><span>#1</span> Chương 1 : Thế cuộc</h1></div>
      <div class="chapter-content"><p>Đoạn đầu tiên của chương.</p><p></p><p>Đoạn thứ hai.</p></div>
    </div></div></main></body></html>`

	chapter, err := Chapter(strings.NewReader(html), "https://tangthuvien.org/lan-kha-ky-duyen/1")
	if err != nil {
		t.Fatal(err)
	}
	if chapter.Number != 1 || chapter.StorySlug != "lan-kha-ky-duyen" {
		t.Fatalf("unexpected chapter: %#v", chapter)
	}
	if chapter.Title != "Chương 1 : Thế cuộc" {
		t.Fatalf("title=%q", chapter.Title)
	}
	if chapter.Content != "Đoạn đầu tiên của chương.\n\nĐoạn thứ hai." || len(chapter.Hash) != 64 {
		t.Fatalf("unexpected content/hash: %#v", chapter)
	}
}

func TestKeyNormalizesVietnamese(t *testing.T) {
	if got := Key("  Đấu  Phá Thương Khung! "); got != "dau-pha-thuong-khung" {
		t.Fatalf("Key()=%q", got)
	}
}
