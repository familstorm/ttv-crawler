# TTV Personal Archiver

*[English](README.md) — bản tiếng Anh là bản chính.*

Crawler Go + Chromium headless + PostgreSQL để lưu truyện từ danh sách
`https://tangthuvien.org/truyen?sort=rate&order=desc` cho mục đích đọc cá nhân.

Hệ thống ưu tiên giảm tải cho website nguồn:

- Dùng Chromium headless cho document request; chỉ truy cập `https://tangthuvien.org` và từ chối redirect sang domain khác.
- Chặn CSS, JavaScript, ảnh, font và media trong browser vì HTML nguồn đã server-rendered.
- Mặc định một request trên toàn tiến trình mỗi 3 giây, cộng jitter ngẫu nhiên 0–1,5 giây.
- Mặc định một worker; kể cả tăng worker, rate limiter vẫn dùng chung.
- Đọc và tuân thủ `robots.txt` theo RFC 9309: tôn trọng `Disallow`/`Allow` và `Crawl-delay`, cache theo host (mặc định 12 giờ). `Crawl-delay` chỉ có thể nâng khoảng cách request, không bao giờ hạ xuống dưới `REQUEST_INTERVAL`. Nếu không đọc được `robots.txt` (lỗi mạng hoặc `5xx`) thì fail-closed, coi như toàn bộ host bị cấm; riêng `404` nghĩa là nguồn không đặt giới hạn nào.
- Tôn trọng `Retry-After`, `429`, `503` và exponential backoff.
- Không đăng nhập, không giải CAPTCHA, không vượt paywall và không gọi API nội bộ của trang.
- Queue lưu trong PostgreSQL, vì vậy có thể dừng bằng `Ctrl+C` rồi chạy tiếp mà không tải lại phần đã hoàn tất.

Bạn vẫn cần tự bảo đảm việc lưu và sử dụng nội dung phù hợp với điều khoản của trang và quy định bản quyền nơi bạn sinh sống. Không nên phát hành lại dữ liệu đã tải.

## Luồng xử lý

1. Seed trực tiếp đủ 284 trang danh mục (`page=1` đến `page=284`), rồi quét hết danh sách để ghi nhận URL truyện trước khi website đóng.
2. Sau khi catalog hết job ưu tiên, chuẩn hoá metadata toàn bộ truyện: tiêu đề, tác giả, mô tả, trạng thái, ảnh bìa, điểm, lượt xem/theo dõi, thể loại và số chương.
3. Chỉ sau khi các job metadata ưu tiên cao hơn đã hết, tải chương theo URL `/{slug}/{số_chương}`.
4. Upsert dữ liệu và SHA-256 nội dung để chạy lại an toàn, không tạo bản ghi trùng.

Queue có phase gate, không chỉ dựa vào priority: `catalog → story → chapter`. Tất cả 284 trang danh mục phải hết `pending/processing` trước khi metadata truyện được claim; toàn bộ job story phải xong trước khi job chapter được claim. Nhờ vậy việc phát hiện danh sách luôn hoàn tất trước khi bắt đầu tải nội dung lớn.

## Chạy nhanh bằng Docker

Yêu cầu Docker có Compose:

```bash
cp .env.example .env
docker compose up -d postgres
docker compose --profile crawler up --build crawler
```

Mở CMS quản trị tối giản để xem queue, phase crawl, danh sách truyện và tiến độ chương:

```bash
docker compose --profile admin up -d --build admin
open http://localhost:8080/admin
```

CMS chỉ hiển thị dữ liệu, không có thao tác xoá/sửa và không bật xác thực; chỉ nên expose trong mạng tin cậy. Menu Queue được tách thành `Danh mục`, `Truyện` và `Chương`, kèm lọc `pending`, `processing`, `completed`, `failed`. Khi chạy Go trực tiếp, dùng `ttv-crawler admin` và đặt `ADMIN_ADDR` (mặc định `127.0.0.1:8080`).

Xem tiến độ ở terminal khác:

```bash
docker compose run --rm crawler status
```

Dừng crawler an toàn:

```bash
docker compose --profile crawler stop crawler
```

PostgreSQL vẫn chạy và dữ liệu được bind mount vào `./volumes/postgres_data` tại root project. Static dùng chung giữa crawler và CMS nằm ở `./volumes/public_data`. `docker compose down` không xoá hai thư mục này; cần backup trước khi tự xoá hoặc thay đổi nội dung bên trong.

## Chạy Go trực tiếp

Yêu cầu Go 1.26+ và PostgreSQL. File `.env` được tự đọc nếu có:

```bash
cp .env.example .env
docker compose up -d postgres
go run ./cmd/ttv-crawler migrate
go run ./cmd/ttv-crawler run
```

Các lệnh CLI:

```text
ttv-crawler migrate       tạo/cập nhật schema
ttv-crawler seed          chỉ thêm START_URL vào queue
ttv-crawler run           seed và chạy worker
ttv-crawler status        xem số job/truyện/chương
ttv-crawler retry-failed  chạy lại job đã hết số lần retry
```

`run` mặc định chờ liên tục khi queue tạm rỗng, phù hợp với container. Đặt `IDLE_EXIT_AFTER=30s` nếu muốn tiến trình tự thoát sau khi queue rỗng 30 giây.

## Cấu hình quan trọng

| Biến | Mặc định | Ý nghĩa |
|---|---:|---|
| `START_URL` | URL danh sách đã cung cấp | Điểm bắt đầu crawl |
| `CATALOG_MAX_PAGE` | `284` | Tổng số trang danh mục được seed ngay khi khởi động |
| `REQUEST_INTERVAL` | `3s` | Khoảng cách tối thiểu toàn cục; code không nhận dưới `1s` |
| `REQUEST_JITTER` | `1.5s` | Jitter ngẫu nhiên cộng thêm |
| `WORKERS` | `1` | Số worker DB/parser, tối đa 8 |
| `HTTP_RETRIES` | `3` | Retry trong một lần xử lý HTTP |
| `BROWSER_EXECUTABLE` | tự dò | Đường dẫn Chromium/Chrome; Docker đã đặt sẵn |
| `ADMIN_ADDR` | `127.0.0.1:8080` | Địa chỉ lắng nghe CMS admin |
| `PUBLIC_DIR` | `public` | Thư mục static dùng để lưu ảnh bìa |
| `COVER_MAX_BYTES` | `5242880` | Kích thước tối đa mỗi ảnh bìa |
| `ROBOTS_CACHE_TTL` | `12h` | Thời gian tái sử dụng `robots.txt` đã parse cho mỗi host |
| `MAX_JOB_ATTEMPTS` | `8` | Retry bền vững của queue |
| `MAX_RESPONSE_BYTES` | `8388608` | Chặn response HTML lớn bất thường |
| `IDLE_EXIT_AFTER` | `0s` | `0` là chờ liên tục |

Không nên giảm `REQUEST_INTERVAL`; nếu website phản hồi chậm hoặc có `429`, hãy tăng lên `5s`–`10s`. Tăng `WORKERS` không làm tăng tần suất HTTP vì mọi worker dùng chung một limiter.

## Dữ liệu đã chuẩn hoá

- `stories`: metadata và số chương dự kiến.
- Ảnh bìa: tải về `/app/public/covers/` trong container, lưu URL local `/static/covers/...` và dùng bind mount `./volumes/public_data` chung với CMS.
- `authors`, `genres`, `story_genres`: quan hệ chuẩn hoá, chống tên/slug trùng.
- `chapters`: tiêu đề, nội dung plain text, số chương và SHA-256.
- `crawl_jobs`: queue có lease, retry, backoff và trạng thái lỗi.
- `source_documents`: HTTP status, ETag/Last-Modified và hash của HTML nguồn, không lưu HTML thô.
- `story_progress`: view tiện theo dõi phần trăm tải.

Ví dụ đọc tiến độ:

```sql
SELECT title, downloaded_chapter_count, expected_chapter_count, progress_percent
FROM story_progress
ORDER BY progress_percent DESC, title;
```

Ví dụ lấy một truyện để đọc/xuất:

```sql
SELECT c.chapter_number, c.title, c.content
FROM chapters c
JOIN stories s ON s.id = c.story_id
WHERE s.source_slug = 'lan-kha-ky-duyen'
ORDER BY c.chapter_number;
```

## Xử lý lỗi

- HTTP `404`, `410` và lỗi client vĩnh viễn được đánh dấu `failed` ngay để tránh gọi lặp vô ích.
- URL bị `robots.txt` từ chối cũng được đánh dấu `failed` ngay, vì retry không thể đổi kết quả.
- `408`, `425`, `429` và lỗi máy chủ/mạng được retry với backoff.
- Shutdown/restart bình thường trả job đang xử lý về `pending` ngay và không tính lượt retry; nếu tiến trình chết đột ngột, job tự về queue sau lease 10 phút.
- Parser trả lỗi rõ ràng nếu không còn tìm thấy tiêu đề/nội dung; trường hợp website đổi HTML, job được giữ lại để retry sau khi cập nhật selector.
- Dùng `retry-failed` sau khi đã sửa parser hoặc sự cố nguồn đã hết.

## Kiểm thử

```bash
go test ./...
go vet ./...
docker compose config --quiet
```

Test bao phủ parser `robots.txt` và hành vi fail-closed, tương tác giữa rate
limiter và `Crawl-delay`, phân loại retry, cùng HTML danh mục, metadata truyện,
số liệu có dấu phân cách hàng nghìn, ngày tiếng Việt và nội dung chương.

## Giấy phép

[MIT](LICENSE). Giấy phép này áp dụng cho mã nguồn, không áp dụng cho nội dung
mà crawler tải về — nội dung đó vẫn thuộc bản quyền gốc. Dự án dành cho mục đích
đọc offline cá nhân; hãy kiểm tra điều khoản của trang nguồn và quy định pháp
luật nơi bạn sinh sống trước khi chạy, và không phát hành lại dữ liệu đã tải.
