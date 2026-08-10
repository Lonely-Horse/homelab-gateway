FROM golang:1.24-alpine AS builder

RUN apk add --no-cache git gcc musl-dev

WORKDIR /app

ENV GOPROXY=https://goproxy.cn,direct

COPY go.mod ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o homelab-telemetry .

FROM alpine:3.18

RUN apk add --no-cache ca-certificates && \
    addgroup -g 1000 -S appgroup && \
    adduser -u 1000 -S appuser

WORKDIR /app

COPY --from=builder /app/homelab-telemetry .

USER appuser

EXPOSE 8085

ENTRYPOINT ["./homelab-telemetry"]