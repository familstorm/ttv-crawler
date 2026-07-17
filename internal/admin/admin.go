package admin

import (
	"context"
	"embed"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/familstorm/crawler-truyen-ttv/internal/model"
	"github.com/familstorm/crawler-truyen-ttv/internal/store"
)

//go:embed templates/*.html
var templateFS embed.FS

const pageSize = 20

type Server struct {
	store     *store.Store
	logger    *slog.Logger
	templates *template.Template
	publicDir string
}

type pageData struct {
	Page        string
	Overview    model.AdminOverview
	Stories     []model.AdminStory
	Jobs        []model.AdminJob
	Search      string
	PageNum     int
	PageCount   int
	Total       int64
	QueueKind   string
	QueueLabel  string
	QueueStatus string
	QueueStats  model.AdminQueueStats
}

func New(s *store.Store, logger *slog.Logger, publicDirs ...string) (*Server, error) {
	templates, err := template.New("page").Funcs(template.FuncMap{
		"number":         formatNumber,
		"urlquery":       url.QueryEscape,
		"add":            func(a, b int) int { return a + b },
		"sub":            func(a, b int) int { return a - b },
		"statusLabel":    statusLabel,
		"statusClass":    statusClass,
		"jobStatusLabel": jobStatusLabel,
		"jobStatusClass": jobStatusClass,
		"kindLabel":      kindLabel,
		"formatTime": func(value time.Time) string {
			if value.IsZero() {
				return "—"
			}
			return value.Local().Format("02/01/2006 15:04")
		},
	}).ParseFS(templateFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	publicDir := "public"
	if len(publicDirs) > 0 && strings.TrimSpace(publicDirs[0]) != "" {
		publicDir = publicDirs[0]
	}
	return &Server{store: s, logger: logger, templates: templates, publicDir: publicDir}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "/admin", http.StatusFound)
	})
	mux.HandleFunc("/admin", s.dashboard)
	mux.HandleFunc("/admin/stories", s.stories)
	mux.HandleFunc("/admin/queue", s.queue)
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir(s.publicDir))))
	mux.HandleFunc("/healthz", s.health)
	return loggingMiddleware(s.logger, mux)
}

func (s *Server) Run(ctx context.Context, addr string) error {
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()
	s.logger.Info("admin CMS đang lắng nghe", "addr", addr)
	err := httpServer.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	overview, err := s.store.AdminOverview(r.Context())
	if err != nil {
		http.Error(w, "Không đọc được trạng thái PostgreSQL", http.StatusInternalServerError)
		return
	}
	s.render(w, pageData{Page: "dashboard", Overview: overview})
}

func (s *Server) stories(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	search := strings.TrimSpace(r.URL.Query().Get("q"))
	page := 1
	if value, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && value > 0 {
		page = value
	}
	stories, total, err := s.store.AdminStories(r.Context(), search, pageSize, (page-1)*pageSize)
	if err != nil {
		http.Error(w, "Không đọc được danh sách truyện", http.StatusInternalServerError)
		return
	}
	pageCount := int((total + pageSize - 1) / pageSize)
	if pageCount == 0 {
		pageCount = 1
	}
	if page > pageCount {
		page = pageCount
	}
	s.render(w, pageData{Page: "stories", Stories: stories, Search: search, PageNum: page, PageCount: pageCount, Total: total})
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if _, err := s.store.Stats(r.Context()); err != nil {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok\n"))
}

func (s *Server) queue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	kind, label, ok := queueKind(r.URL.Query().Get("kind"))
	if !ok {
		http.Error(w, "loại queue không hợp lệ", http.StatusBadRequest)
		return
	}
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if status != "" && !validJobStatus(status) {
		http.Error(w, "trạng thái queue không hợp lệ", http.StatusBadRequest)
		return
	}
	page := 1
	if value, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && value > 0 {
		page = value
	}
	jobs, total, stats, err := s.store.AdminJobs(r.Context(), kind, status, pageSize, (page-1)*pageSize)
	if err != nil {
		http.Error(w, "Không đọc được queue", http.StatusInternalServerError)
		return
	}
	pageCount := int((total + pageSize - 1) / pageSize)
	if pageCount == 0 {
		pageCount = 1
	}
	if page > pageCount {
		page = pageCount
	}
	s.render(w, pageData{Page: "queue", Jobs: jobs, PageNum: page, PageCount: pageCount, Total: total,
		QueueKind: string(kind), QueueLabel: label, QueueStatus: status, QueueStats: stats})
}

func (s *Server) render(w http.ResponseWriter, data pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, "page", data); err != nil {
		s.logger.Error("render admin thất bại", "error", err)
	}
}

func loggingMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		next.ServeHTTP(w, r)
		logger.Debug("admin request", "method", r.Method, "path", r.URL.Path, "duration", time.Since(started))
	})
}

func methodNotAllowed(w http.ResponseWriter) {
	w.Header().Set("Allow", http.MethodGet)
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

func formatNumber(value any) string {
	var number int64
	switch typed := value.(type) {
	case int:
		number = int64(typed)
	case int64:
		number = typed
	default:
		return "0"
	}
	sign := ""
	if number < 0 {
		sign = "-"
		number = -number
	}
	digits := strconv.FormatInt(number, 10)
	for index := len(digits) - 3; index > 0; index -= 3 {
		digits = digits[:index] + "." + digits[index:]
	}
	return sign + digits
}

func statusLabel(status string) string {
	switch status {
	case "updating":
		return "Đang cập nhật"
	case "completed":
		return "Hoàn thành"
	case "paused":
		return "Tạm dừng"
	default:
		return "Chưa rõ"
	}
}

func statusClass(status string) string {
	switch status {
	case "completed":
		return "success"
	case "paused":
		return "muted"
	default:
		return "warning"
	}
}

func queueKind(value string) (model.JobKind, string, bool) {
	switch value {
	case string(model.JobCatalog):
		return model.JobCatalog, "Danh mục", true
	case string(model.JobStory):
		return model.JobStory, "Truyện", true
	case string(model.JobChapter):
		return model.JobChapter, "Chương", true
	default:
		return "", "", false
	}
}

func kindLabel(value string) string {
	_, label, ok := queueKind(value)
	if !ok {
		return value
	}
	return label
}

func validJobStatus(status string) bool {
	switch status {
	case "pending", "processing", "completed", "failed":
		return true
	default:
		return false
	}
}

func jobStatusLabel(status string) string {
	switch status {
	case "pending":
		return "Đang chờ"
	case "processing":
		return "Đang chạy"
	case "completed":
		return "Hoàn tất"
	case "failed":
		return "Lỗi"
	default:
		return status
	}
}

func jobStatusClass(status string) string {
	switch status {
	case "completed":
		return "success"
	case "failed":
		return "danger"
	case "processing":
		return "info"
	default:
		return "warning"
	}
}
