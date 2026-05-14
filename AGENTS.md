# AGENTS.md

## Projekt

EEG Sumsum ist ein simples serverseitig gerendertes Teilnehmerportal fuer eine Energiegemeinschaft.

- Sprache: Go
- UI: SSR, keine SPA, kein clientseitiger Router
- Datenbank: SQLite
- Importquelle: EDA/EEG XLSX-Reports mit Sheets `Übersicht` und `Energiedaten`
- Teilnehmer sehen nur zugewiesene Zaehlpunkte; Administratoren sehen alles

## Wichtige Vorgaben

- Vor Commits immer auf einer Feature-Branch arbeiten. Fuer dieses Feature: `feature/participant-portal`.
- Bestehende Aenderungen nicht ungefragt revertieren.
- Keine Secrets committen.
- Timestamps aus XLSX als `Europe/Vienna` parsen und UTC speichern.
- XLSX-Uploads muessen idempotent sein: keine Duplikate bei gleicher Datei oder ueberlappenden Zeitraeumen.
- Eindeutiger Messwert-Schluessel: `metering_point_id + direction + metric_key + interval_start`.
- Teilnehmer duerfen `TOTAL`-Daten und nicht zugewiesene Zaehlpunkte nicht sehen.

## Lokale Entwicklung

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

Weitere relevante ENV:

- `SESSION_SECRET`: ausserhalb von `APP_ENV=dev` erforderlich
- `ADMIN_USERNAME` / `ADMIN_PASSWORD`: erzeugt oder aktualisiert Admin beim Start
- `ADMIN_API_TOKEN`: optionaler Bearer Token fuer XLSX Upload API
- `EDA_BASE_URL`: EDA API fuer Login und Energy-Community Details
- `EDA_SERIES_BASE_URL`: EDA API fuer `consumptionsurya/g` und `consumptionsurya/p`

## API

XLSX Upload:

```sh
curl -H "Authorization: Bearer dev-token" \
  -F "file=@./imports/report.xlsx" \
  http://localhost:8080/api/admin/imports
```

Endpoint:

- `POST /api/admin/imports`
- Auth: `Authorization: Bearer <token>`
- Body: multipart field `file`
- Response: JSON mit Insert/Update/Skip-Zaehlung

EDA Portal Endpunkte, Host-Split und Response-Shapes sind in `docs/eda-api.md` dokumentiert.

## Architektur

- `cmd/server`: Server-Entrypoint, Config, Bootstrap, HTTP start/stop
- `internal/config`: ENV-Konfiguration
- `internal/db`: SQLite Schema, Models, Queries, Upserts
- `internal/auth`: bcrypt Passwort- und API-Token-Handling
- `internal/imports`: XLSX Parser und Import-Workflow
- `internal/web`: chi Router, Middleware, Handler
- `internal/views`: SSR HTML Views mit `templ.ComponentFunc`
- `internal/charts`: serverseitige Inline-SVG-Charts

## Datenmodell

Wichtige Tabellen:

- `users`: Login, Anzeigename, bcrypt Hash, Rolle `admin|participant`, Aktivstatus
- `metering_points`: Zaehlpunkt, Anzeigename, Richtung, Netzbetreiber
- `user_metering_points`: many-to-many Account-Zaehlpunkt-Zuordnung
- `api_tokens`: bcrypt-gehashte Upload-Tokens
- `import_batches`: Dateiname, SHA-256, Report-/Datenzeitraum
- `measurements`: Viertelstundenwerte, dedupliziert per Unique Constraint
- `overview_summaries`: Summen aus `Übersicht`

## XLSX Struktur

Bekannte Workbook-Form:

- Sheet `Energiedaten`
  - Metadaten in Zeilen 2 bis 16
  - Daten ab Zeile 17
  - Spaltenpaare: Wertspalte + Qualitaetsspalte
  - `MeteringPointId` in Zeile 2
  - `Energydirection` in Zeile 4
  - Report-Zeitraum in Zeilen 5 und 6
  - Data-Zeitraum in Zeilen 7 und 8
  - Messgroesse in Zeile 14
- Sheet `Übersicht`
  - Header in Zeile 6
  - Summen je Zaehlpunkt ab Zeile 7

## Tests und Checks

Vor Abschluss ausfuehren:

```sh
gofmt -w cmd internal
go test ./...
```

Aktuelle Tests decken ab:

- Parser fuer das bereitgestellte XLSX
- Idempotenter Re-Import derselben Datei
- Fehlende XLSX-Sheets
- Teilnehmerzugriff auf Adminbereich
- Teilnehmerzugriff auf nicht zugewiesene Zaehlpunkte
- Ungueltiger API Token

## UI Leitplanken

- SSR-only beibehalten.
- Keine SPA-Abhaengigkeiten einfuehren.
- Formulare als normale POSTs bauen.
- Visualisierungen serverseitig erzeugen, aktuell als Inline-SVG.
- Adminbereich dient fuer Benutzer, Rollen und Zaehlpunkt-Zuordnungen.
