# EEG Sumsum

SSR-Portal fuer Energiegemeinschaften: XLSX-Reports und EDA-Portal-Daten importieren, Messwerte idempotent in SQLite speichern und Teilnehmern nur ihre zugewiesenen Zaehlpunkte anzeigen.

## Features

- Klassisch serverseitig gerenderte Web UI, keine SPA
- Adminbereich fuer XLSX Uploads, EDA Imports, Benutzer und Zaehlpunkt-Zuordnungen
- Teilnehmerportal mit Zugriffsschutz pro Zaehlpunkt
- SQLite-Speicherung mit deduplizierten Viertelstundenwerten
- Server-seitige Inline-SVG-Charts
- Optionaler taeglicher EDA Auto-Import

## Stack

- Go 1.26
- chi
- SQLite via `modernc.org/sqlite`
- Excelize
- SCS Sessions
- bcrypt
- OCI Container mit `Containerfile`

## Schnellstart

```sh
ADMIN_USERNAME=admin \
ADMIN_PASSWORD=admin12345 \
ADMIN_API_TOKEN=dev-token \
go run ./cmd/server
```

Danach:

- Web UI: <http://localhost:8080>
- Login: `admin` / `admin12345`
- Healthcheck: <http://localhost:8080/healthz>

XLSX Upload per API:

```sh
curl -H "Authorization: Bearer dev-token" \
  -F "file=@./imports/report.xlsx" \
  http://localhost:8080/api/admin/imports
```

EDA Import per API:

```sh
curl -H "Authorization: Bearer dev-token" \
  -H "Content-Type: application/json" \
  -d '{"from":"2026-05-01","to":"2026-05-14"}' \
  http://localhost:8080/api/admin/eda-imports
```

## Dokumentation

- [Konfiguration](docs/configuration.md)
- [Deployment](docs/deployment.md)
- [App API](docs/app-api.md)
- [EDA Portal API](docs/eda-api.md)
- [Datenmodell](docs/data-model.md)
- [Entwicklung und Tests](docs/development.md)

## Projektstruktur

```text
cmd/server        HTTP Server Entrypoint
cmd/eda-verify    EDA API Diagnosewerkzeug
internal/auth     Login, bcrypt, API Token
internal/charts   Inline-SVG Charts
internal/config   ENV-Konfiguration
internal/db       SQLite Schema und Queries
internal/imports  XLSX Parser und Importlogik
internal/views    SSR HTML Views
internal/web      Router, Middleware, Handler
docs              Detaildokumentation
```
