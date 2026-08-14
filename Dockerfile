FROM golang:1.26-alpine AS builder

ENV GOPROXY=https://mirrors.aliyun.com/goproxy/,direct
ENV GOFLAGS="-buildvcs=false"

WORKDIR /build
COPY backend/go.mod backend/go.sum ./
RUN go mod download

COPY backend/ .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o codeguard ./cmd/main.go

FROM alpine:latest
RUN apk --no-cache add ca-certificates tzdata wget git && \
    cp /usr/share/zoneinfo/Asia/Shanghai /etc/localtime && \
    echo "Asia/Shanghai" > /etc/timezone
WORKDIR /app
COPY --from=builder /build/codeguard .
COPY prototype /app/prototype

ENV FRONTEND_PATH=/app/prototype
ENV TZ=Asia/Shanghai

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:8080/health || exit 1

CMD ["./codeguard"]