# syntax=docker/dockerfile:1

FROM golang:1.26-bookworm AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/eeg-sumsum ./cmd/server

FROM debian:bookworm-slim

RUN apt-get update \
  && apt-get install -y --no-install-recommends ca-certificates curl tzdata \
  && rm -rf /var/lib/apt/lists/*

RUN groupadd --gid 10001 app \
  && useradd --uid 10001 --gid 10001 --home-dir /app --create-home --shell /usr/sbin/nologin app

WORKDIR /app
COPY --from=build /out/eeg-sumsum /usr/local/bin/eeg-sumsum

RUN mkdir -p /data && chown -R app:app /data /app

USER app

ENV APP_ENV=production \
    ADDR=:8080 \
    DATABASE_PATH=/data/eeg.db \
    TZ=Europe/Vienna

VOLUME /data

EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/eeg-sumsum"]
