package parser

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/familstorm/crawler-truyen-ttv/internal/model"
)

var (
	storyAuthorRE = regexp.MustCompile(`(?i)\s+-\s+Tác giả\s+(.+?)\s+\|`)
	viewsRE       = regexp.MustCompile(`(?i)([\d.,]+)\s*Lượt\s*Xem`)
	chaptersRE    = regexp.MustCompile(`(?i)([\d.,]+)\s*Chương`)
	followersRE   = regexp.MustCompile(`(?i)([\d.,]+)\s*Người\s*Theo\s*Dõi`)
	ratingRE      = regexp.MustCompile(`(?i)(\d+(?:[.,]\d+)?)\s*\(([\d.,]+)\s*đánh\s*giá\)`)
	catalogRateRE = regexp.MustCompile(`\b(\d+(?:[.,]\d+)?)\b`)
	chapterNoRE   = regexp.MustCompile(`(?i)^#?\s*\d+\s*`)
	dateRE        = regexp.MustCompile(`(?i)(\d{1,2})\s+tháng\s+(\d{1,2})\s+năm\s+(\d{4})`)
)

var reservedPaths = map[string]struct{}{
	"": {}, "truyen": {}, "bang-xep-hang": {}, "sitemap.xml": {},
	"login": {}, "register": {}, "api": {}, "favicon.ico": {},
}

func Catalog(r io.Reader, pageURL string) (model.CatalogPage, error) {
	doc, err := goquery.NewDocumentFromReader(r)
	if err != nil {
		return model.CatalogPage{}, fmt.Errorf("đọc HTML danh mục: %w", err)
	}
	base, err := url.Parse(pageURL)
	if err != nil {
		return model.CatalogPage{}, fmt.Errorf("URL danh mục: %w", err)
	}

	result := model.CatalogPage{}
	seen := make(map[string]struct{})
	doc.Find("main h3").Each(func(_ int, h *goquery.Selection) {
		link := h.ParentFiltered("a")
		if link.Length() == 0 {
			link = h.ParentsFiltered("a").First()
		}
		href, ok := link.Attr("href")
		if !ok {
			return
		}
		absolute, slug, ok := storyURL(base, href)
		if !ok {
			return
		}
		if _, exists := seen[absolute]; exists {
			return
		}

		card := h
		for range 6 {
			if card.Find("img").Length() > 0 && card.Find("p").Length() > 0 {
				break
			}
			card = card.Parent()
			if card.Length() == 0 {
				return
			}
		}
		if card.Find("img").Length() == 0 || card.Find("p").Length() == 0 {
			return
		}

		title := Text(h.Text())
		if title == "" {
			return
		}
		story := model.CatalogStory{
			URL:             absolute,
			Slug:            slug,
			Title:           title,
			NormalizedTitle: Key(title),
			Summary:         Text(card.Find("p").First().Text()),
		}
		if src, exists := card.Find("img").First().Attr("src"); exists {
			story.CoverURL = imageURL(base, src)
		}
		ratingText := Text(card.Find(`svg[class*="lucide-star"]`).First().Parent().Text())
		if match := catalogRateRE.FindStringSubmatch(ratingText); len(match) == 2 {
			if value, parseErr := strconv.ParseFloat(strings.ReplaceAll(match[1], ",", "."), 64); parseErr == nil && value <= 5 {
				story.Rating = &value
			}
		}
		seen[absolute] = struct{}{}
		result.Stories = append(result.Stories, story)
	})

	if next, ok := doc.Find(`nav[aria-label="Pagination"] a[aria-label="Next page"]`).Attr("href"); ok {
		result.NextURL = resolveURL(base, next)
	}
	if len(result.Stories) == 0 {
		return model.CatalogPage{}, errors.New("không tìm thấy truyện nào; cấu trúc trang có thể đã thay đổi")
	}
	return result, nil
}

func Story(r io.Reader, storyURLValue string) (model.Story, error) {
	doc, err := goquery.NewDocumentFromReader(r)
	if err != nil {
		return model.Story{}, fmt.Errorf("đọc HTML truyện: %w", err)
	}
	base, err := url.Parse(storyURLValue)
	if err != nil {
		return model.Story{}, fmt.Errorf("URL truyện: %w", err)
	}
	slug := strings.Trim(base.Path, "/")
	h1 := doc.Find("main h1").First()
	title := Text(h1.Text())
	if title == "" {
		return model.Story{}, errors.New("không tìm thấy tiêu đề truyện")
	}

	story := model.Story{
		URL:             base.String(),
		Slug:            slug,
		Title:           title,
		NormalizedTitle: Key(title),
		Status:          "unknown",
	}
	if match := storyAuthorRE.FindStringSubmatch(Text(doc.Find("title").Text())); len(match) == 2 {
		story.Author = Text(match[1])
		story.NormalizedAuthor = Key(story.Author)
	}

	header := h1.Parent()
	headerText := Text(header.Text())
	story.ViewCount = matchCount(viewsRE, headerText)
	story.FollowerCount = matchCount(followersRE, headerText)
	story.ExpectedChapterCount = int(matchCount(chaptersRE, headerText))
	if match := ratingRE.FindStringSubmatch(headerText); len(match) == 3 {
		if rating, parseErr := strconv.ParseFloat(strings.ReplaceAll(match[1], ",", "."), 64); parseErr == nil {
			story.Rating = &rating
		}
		story.RatingCount = int(parseCount(match[2]))
	}
	story.Status = parseStatus(header.ChildrenFiltered("div").First().Text())

	coverRoot := h1.Parent().Parent()
	if src, exists := coverRoot.Find("img").First().Attr("src"); exists {
		story.CoverURL = imageURL(base, src)
	} else if src, exists := doc.Find(`meta[property="og:image"]`).Attr("content"); exists {
		story.CoverURL = resolveURL(base, src)
	}

	doc.Find("main h2").EachWithBreak(func(_ int, heading *goquery.Selection) bool {
		if Text(heading.Text()) != "Tóm Tắt" {
			return true
		}
		var paragraphs []string
		heading.Parent().Find("p").Each(func(_ int, p *goquery.Selection) {
			if value := Text(p.Text()); value != "" {
				paragraphs = append(paragraphs, value)
			}
		})
		story.Summary = strings.Join(paragraphs, "\n\n")
		return false
	})

	seenGenres := make(map[string]struct{})
	doc.Find(`main a[href^="/truyen?genre="]`).Each(func(_ int, a *goquery.Selection) {
		href, _ := a.Attr("href")
		parsed, parseErr := url.Parse(href)
		if parseErr != nil {
			return
		}
		genreSlug := Key(parsed.Query().Get("genre"))
		genreName := Text(a.Text())
		if genreSlug == "" || genreName == "" {
			return
		}
		if _, exists := seenGenres[genreSlug]; exists {
			return
		}
		seenGenres[genreSlug] = struct{}{}
		story.Genres = append(story.Genres, model.Genre{Name: genreName, Slug: genreSlug})
	})

	if rawDate := valueAfterLabel(doc, "Ngày Thêm"); rawDate != "" {
		story.SourceCreatedAt = parseVietnameseDate(rawDate)
	}
	return story, nil
}

func Chapter(r io.Reader, chapterURL string) (model.Chapter, error) {
	doc, err := goquery.NewDocumentFromReader(r)
	if err != nil {
		return model.Chapter{}, fmt.Errorf("đọc HTML chương: %w", err)
	}
	parsed, err := url.Parse(chapterURL)
	if err != nil {
		return model.Chapter{}, fmt.Errorf("URL chương: %w", err)
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 2 {
		return model.Chapter{}, errors.New("URL chương không đúng dạng /slug/số")
	}
	number, err := strconv.Atoi(parts[1])
	if err != nil || number < 1 {
		return model.Chapter{}, errors.New("số chương không hợp lệ")
	}

	h1 := doc.Find("main h1").First()
	if h1.Length() == 0 {
		return model.Chapter{}, errors.New("không tìm thấy tiêu đề chương")
	}
	title := Text(chapterNoRE.ReplaceAllString(Text(h1.Text()), ""))
	if title == "" {
		title = fmt.Sprintf("Chương %d", number)
	}
	card := h1.Parent().Parent()
	var paragraphs []string
	card.Find("p").Each(func(_ int, p *goquery.Selection) {
		if value := Text(p.Text()); value != "" {
			paragraphs = append(paragraphs, value)
		}
	})
	content := strings.Join(paragraphs, "\n\n")
	if len([]rune(content)) < 20 {
		return model.Chapter{}, errors.New("nội dung chương rỗng hoặc quá ngắn; cấu trúc trang có thể đã thay đổi")
	}
	hash := sha256.Sum256([]byte(content))
	return model.Chapter{
		StorySlug: parts[0],
		URL:       parsed.String(),
		Number:    number,
		Title:     title,
		Content:   content,
		Hash:      hex.EncodeToString(hash[:]),
	}, nil
}

func storyURL(base *url.URL, href string) (absolute, slug string, ok bool) {
	resolved, err := base.Parse(href)
	if err != nil || resolved.Hostname() != base.Hostname() || resolved.Scheme != "https" {
		return "", "", false
	}
	path := strings.Trim(resolved.Path, "/")
	if strings.Contains(path, "/") {
		return "", "", false
	}
	if _, reserved := reservedPaths[path]; reserved {
		return "", "", false
	}
	resolved.RawQuery = ""
	resolved.Fragment = ""
	return resolved.String(), path, true
}

func resolveURL(base *url.URL, raw string) string {
	resolved, err := base.Parse(raw)
	if err != nil {
		return ""
	}
	resolved.Fragment = ""
	return resolved.String()
}

func imageURL(base *url.URL, raw string) string {
	resolved, err := base.Parse(raw)
	if err != nil {
		return ""
	}
	if resolved.Path == "/_next/image" {
		if original := resolved.Query().Get("url"); original != "" {
			return original
		}
	}
	return resolved.String()
}

func matchCount(re *regexp.Regexp, value string) int64 {
	match := re.FindStringSubmatch(value)
	if len(match) != 2 {
		return 0
	}
	return parseCount(match[1])
}

func parseCount(value string) int64 {
	value = strings.NewReplacer(",", "", ".", "", " ", "").Replace(value)
	n, _ := strconv.ParseInt(value, 10, 64)
	return n
}

func parseStatus(value string) string {
	value = strings.ToLower(Text(value))
	switch {
	case strings.Contains(value, "hoàn thành") || strings.Contains(value, "đã hoàn"):
		return "completed"
	case strings.Contains(value, "đang cập nhật"):
		return "updating"
	case strings.Contains(value, "tạm ngưng") || strings.Contains(value, "tạm dừng"):
		return "paused"
	default:
		return "unknown"
	}
}

func valueAfterLabel(doc *goquery.Document, label string) string {
	var result string
	doc.Find("main *").EachWithBreak(func(_ int, s *goquery.Selection) bool {
		if s.Children().Length() != 0 || !strings.EqualFold(Text(s.Text()), label) {
			return true
		}
		if next := s.Next(); next.Length() > 0 {
			result = Text(next.Text())
			return false
		}
		if parentNext := s.Parent().Next(); parentNext.Length() > 0 {
			result = Text(parentNext.Text())
			return false
		}
		return true
	})
	return result
}

func parseVietnameseDate(value string) *time.Time {
	match := dateRE.FindStringSubmatch(Text(value))
	if len(match) != 4 {
		return nil
	}
	day, _ := strconv.Atoi(match[1])
	month, _ := strconv.Atoi(match[2])
	year, _ := strconv.Atoi(match[3])
	if day < 1 || day > 31 || month < 1 || month > 12 {
		return nil
	}
	parsed := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	return &parsed
}
