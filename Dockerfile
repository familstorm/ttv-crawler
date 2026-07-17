FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/ttv-crawler ./cmd/ttv-crawler

FROM alpine:3.23
RUN apk add --no-cache ca-certificates chromium dumb-init tzdata \
    && addgroup -S crawler \
    && adduser -S -G crawler crawler \
    && mkdir -p /app/public/covers \
    && chown -R crawler:crawler /app
WORKDIR /app
ENV BROWSER_EXECUTABLE=/usr/bin/chromium-browser
COPY --from=build /out/ttv-crawler /usr/local/bin/ttv-crawler
USER crawler:crawler
ENTRYPOINT ["/usr/bin/dumb-init", "--", "/usr/local/bin/ttv-crawler"]
CMD ["run"]
