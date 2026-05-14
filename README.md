# EEG Sumsum

Modernes SSR-Portal fuer Energiegemeinschaften: XLSX-Reports importieren, Messwerte sauber in SQLite speichern und Teilnehmern genau die Zaehlpunkte anzeigen, die ihnen zugeordnet sind.

## Was die App macht

- Importiert EDA/EEG XLSX-Dateien mit `Übersicht` und `Energiedaten`
- Importiert aggregierte EDA-Portal-Daten ueber die EDA API
- Speichert Viertelstundenwerte idempotent in SQLite
- Verhindert Duplikate bei mehrfachen oder ueberlappenden Uploads
- Bietet Login fuer Teilnehmer und Administratoren
- Erlaubt Admins, Zaehlpunkte Benutzerkonten zuzuordnen
- Rendert Seiten klassisch serverseitig: kein SPA, kein clientseitiger Router
- Zeichnet Zeitreihen als serverseitige Inline-SVG-Charts

## Stack

- Go 1.26
- chi fuer Routing
- SSR HTML Views
- SQLite via `modernc.org/sqlite`
- Excelize fuer XLSX Parsing
- SCS Sessions
- bcrypt fuer Passwoerter und API Tokens
- OCI Container mit `Containerfile`

## Schnellstart lokal

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

## Deployment mit Podman

### Variante A: Release-Image aus GHCR

Wenn der GitHub Workflow gelaufen ist, steht das Multi-Arch-Image als `latest` in GHCR bereit:

```sh
podman pull ghcr.io/<owner>/<repo>:latest
```

Starten:

```sh
podman volume create eeg-sumsum-data

podman run -d --name eeg-sumsum \
  -p 8080:8080 \
  -v eeg-sumsum-data:/data \
  -e APP_ENV=production \
  -e SESSION_SECRET="replace-with-a-long-random-secret" \
  -e ADMIN_USERNAME=admin \
  -e ADMIN_PASSWORD="replace-with-a-strong-password" \
  -e ADMIN_API_TOKEN="replace-with-a-random-upload-token" \
  ghcr.io/<owner>/<repo>:latest
```

### Variante B: Podman Compose

Lege eine `.env` Datei an:

```sh
SESSION_SECRET=replace-with-a-long-random-secret
ADMIN_USERNAME=admin
ADMIN_PASSWORD=replace-with-a-strong-password
ADMIN_API_TOKEN=replace-with-a-random-upload-token
PORT=8080
```

Starten:

```sh
podman compose -f compose.yml up --build -d
```

Logs:

```sh
podman compose -f compose.yml logs -f
```

Stoppen:

```sh
podman compose -f compose.yml down
```

### Variante C: Lokal bauen und mit Podman starten

Image bauen:

```sh
podman build -f Containerfile -t eeg-sumsum:local .
```

Persistentes Volume anlegen:

```sh
podman volume create eeg-sumsum-data
```

Container starten:

```sh
podman run -d --name eeg-sumsum \
  -p 8080:8080 \
  -v eeg-sumsum-data:/data \
  -e APP_ENV=production \
  -e SESSION_SECRET="replace-with-a-long-random-secret" \
  -e ADMIN_USERNAME=admin \
  -e ADMIN_PASSWORD="replace-with-a-strong-password" \
  -e ADMIN_API_TOKEN="replace-with-a-random-upload-token" \
  eeg-sumsum:local
```

Healthcheck:

```sh
curl -fsS http://localhost:8080/healthz
```

## Konfiguration

| Variable | Default | Beschreibung |
| --- | --- | --- |
| `ADDR` | `:8080` | HTTP Listen-Adresse im Container oder lokal |
| `DATABASE_PATH` | `./data/eeg.db` lokal, `/data/eeg.db` im Container | SQLite-Datenbank |
| `APP_ENV` | `dev` lokal, `production` im Container | Steuert sichere Defaults |
| `SESSION_SECRET` | leer | In Production erforderlich |
| `ADMIN_USERNAME` | leer | Erstellt oder aktualisiert Admin beim Start |
| `ADMIN_PASSWORD` | leer | Passwort fuer Bootstrap-Admin |
| `ADMIN_API_TOKEN` | leer | Bearer Token fuer XLSX Uploads |
| `EDA_BASE_URL` | `https://prod-api.eda-portal.at/api` | EDA API Basis-URL |
| `EDA_USERNAME` | leer | EDA Portal Login |
| `EDA_PASSWORD` | leer | EDA Portal Passwort |
| `EDA_COMMUNITY_ID` | leer | Interne EDA Energy-Community-ID |
| `EDA_METERING_POINT_ID` | `EDA_<EDA_COMMUNITY_ID>` | Lokale synthetische Zaehlpunkt-ID fuer EDA Aggregatsdaten |
| `EDA_METERING_POINT_NAME` | `EDA Community <EDA_COMMUNITY_ID>` | Anzeigename fuer den synthetischen EDA Zaehlpunkt |
| `EDA_GROUP_BY` | `day` | Aktuell fuer Import nur `day` unterstuetzt |

## API

`POST /api/admin/imports`

- Auth: `Authorization: Bearer <ADMIN_API_TOKEN>`
- Body: `multipart/form-data`
- Feld: `file`
- Ergebnis: JSON mit `measurements_inserted`, `measurements_updated`, `measurements_skipped`

Beispiel:

```sh
curl -H "Authorization: Bearer $ADMIN_API_TOKEN" \
  -F "file=@./imports/report.xlsx" \
  http://localhost:8080/api/admin/imports
```

`POST /api/admin/eda-imports`

- Auth: `Authorization: Bearer <ADMIN_API_TOKEN>`
- Body: optional JSON mit `from` und `to`
- Datumsformat: `YYYY-MM-DD`, `YYYY-MM-DDTHH:MM` oder RFC3339
- Ohne Body wird der Vortag in `Europe/Vienna` importiert
- Ergebnis: JSON mit `measurements_inserted`, `measurements_updated`, `measurements_skipped`

Beispiel:

```sh
curl -H "Authorization: Bearer $ADMIN_API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"from":"2026-05-01","to":"2026-05-14"}' \
  http://localhost:8080/api/admin/eda-imports
```

Der EDA Import speichert die aggregierten Community-Zeitreihen unter einem synthetischen Zaehlpunkt. Standardmaessig ist das `EDA_<EDA_COMMUNITY_ID>`. Die EDA-Zeitstempel werden als `Europe/Vienna` interpretiert und UTC gespeichert.

## Datenmodell in Kurzform

- `users`: Admin- und Teilnehmerkonten
- `metering_points`: Zaehlpunkte
- `user_metering_points`: Zuordnung Account zu Zaehlpunkten
- `api_tokens`: gehashte Upload-Tokens
- `import_batches`: importierte Dateien und Zeitraeume
- `measurements`: Viertelstundenwerte
- `overview_summaries`: Summen aus dem Sheet `Übersicht`

Messwerte sind eindeutig ueber:

```text
metering_point_id + direction + metric_key + interval_start
```

## Entwicklung

```sh
gofmt -w cmd internal
go test ./...
```

Optionaler Parser-Test mit echter XLSX-Datei:

```sh
EEG_SAMPLE_XLSX=./imports/report.xlsx go test ./internal/imports
```

## Projektstruktur

```text
cmd/server        HTTP Server Entrypoint
internal/auth     Login, bcrypt, API Token
internal/charts   Inline-SVG Charts
internal/config   ENV-Konfiguration
internal/db       SQLite Schema und Queries
internal/imports  XLSX Parser und Importlogik
internal/views    SSR HTML Views
internal/web      Router, Middleware, Handler
```
