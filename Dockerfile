# Builder: Go binary
FROM golang:1.24.5-alpine AS builder
WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

# Projenin tamamı (migrations da dahil!)
COPY . .

# main.go embed kullanıyorsa build sırasında migrations/ klasörü olmalı
RUN CGO_ENABLED=0 GOOS=linux go build -o main -ldflags="-s -w" ./cmd/server/main.go

# Runtime: Küçük, güvenli, non-root
FROM alpine:3.22
WORKDIR /app

COPY --from=builder /app/main .

# Config dosyalarını runtime'a kopyala (embed binary için gerek yok, ama config loading için)
COPY --from=builder /app/configs ./configs
# Migrations embed edildiği için runtime copy kaldırıldı

# Güvenlik için non-root user
RUN addgroup -g 1001 appgroup && adduser -D -u 1001 -G appgroup appuser
USER appuser

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget --no-verbose --tries=1 http://localhost:8080/health || exit 1

CMD ["./main"]