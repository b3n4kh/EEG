# Entwicklung und Tests

## Lokaler Server

```sh
ADMIN_USERNAME=admin \
ADMIN_PASSWORD=admin12345 \
ADMIN_API_TOKEN=dev-token \
go run ./cmd/server
```

Standardwerte:

- `ADDR=:8080`
- `DATABASE_PATH=./data/eeg.db`
- `APP_ENV=dev`

## Checks

Vor Abschluss ausfuehren:

```sh
gofmt -w cmd internal
go test ./...
```

Optional mit Race/Coverage:

```sh
go test ./... -race -covermode=atomic -coverprofile=coverage.out
go tool cover -func=coverage.out
```

Optionaler Parser-Test mit echter XLSX-Datei:

```sh
EEG_SAMPLE_XLSX=./imports/report.xlsx go test ./internal/imports
```

## Pre-Commit Hooks

Optional Git-Hooks mit `prek` installieren:

```sh
uv tool install prek
prek install
prek run --all-files
```

Die `prek` Hooks formatieren Go-Code, halten `go.mod`/`go.sum` ordentlich, pruefen YAML/Hygiene, laufen mit `go vet`, fuehren Staticcheck aus und starten `go test ./...`.

## Testabdeckung

Aktuelle Tests decken unter anderem ab:

- Parser fuer bereitgestellte XLSX-Dateien
- Idempotenter Re-Import derselben Datei
- Fehlende XLSX-Sheets
- Teilnehmerzugriff auf Adminbereich
- Teilnehmerzugriff auf nicht zugewiesene Zaehlpunkte
- Ungueltiger API Token
- EDA Import gegen Fake-EDA-Server
- Automatischer EDA Import und Statusanzeige

Die End-to-End-Szenarien laufen Go-nativ als Teil von `go test ./...` unter `internal/e2e`. Sie starten den echten Router mit temporaeren SQLite-Datenbanken und pruefen Login, XLSX-Upload, API-Importe, Teilnehmer-Sichtbarkeit und EDA-Import gegen einen Fake-EDA-Server.
