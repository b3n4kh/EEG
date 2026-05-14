# Konfiguration

## Standardwerte

| Variable | Default | Beschreibung |
| --- | --- | --- |
| `ADDR` | `:8080` | HTTP Listen-Adresse im Container oder lokal |
| `DATABASE_PATH` | `./data/eeg.db` lokal, `/data/eeg.db` im Container | SQLite-Datenbank |
| `APP_ENV` | `dev` lokal, `production` im Container | Steuert sichere Defaults |
| `LOG_LEVEL` | `debug` in dev, sonst `info` | Log-Level: `debug`, `info`, `warn`, `error` |
| `SESSION_SECRET` | leer | In Production erforderlich |
| `ADMIN_USERNAME` | leer | Erstellt oder aktualisiert Admin beim Start |
| `ADMIN_PASSWORD` | leer | Passwort fuer Bootstrap-Admin |
| `ADMIN_API_TOKEN` | leer | Bearer Token fuer Import APIs |
| `EDA_BASE_URL` | `https://prod-api.eda-portal.at/api` | EDA API Basis-URL fuer Login und Energy-Community Details |
| `EDA_SERIES_BASE_URL` | `https://prod.eda-portal.at/api` | EDA API Basis-URL fuer einzelne Zaehlpunkt-Messreihen |
| `EDA_USERNAME` | leer | EDA Portal Login |
| `EDA_PASSWORD` | leer | EDA Portal Passwort |
| `EDA_COMMUNITY_ID` | leer | Interne EDA Energy-Community-ID |
| `EDA_METERING_POINTS` | leer | Optionaler Fallback, falls die Community-Detail-API keine Zaehlpunkte liefert: `ZP_ID:CONSUMPTION,ZP_ID:GENERATION` |
| `EDA_AUTO_IMPORT_ENABLED` | `true` | Aktiviert den taeglichen automatischen EDA Import, sobald EDA konfiguriert ist |
| `EDA_AUTO_IMPORT_CRON` | `15 3 * * *` | Cron-Zeitplan fuer den automatischen EDA Import in `Europe/Vienna` |

## Lokale Entwicklung

```sh
ADMIN_USERNAME=admin \
ADMIN_PASSWORD=admin12345 \
ADMIN_API_TOKEN=dev-token \
go run ./cmd/server
```

## EDA Import

EDA ist aktiv, sobald `EDA_USERNAME`, `EDA_PASSWORD` und `EDA_COMMUNITY_ID` gesetzt sind.

Der Import liest die Zaehlpunkte aus der Energy-Community Detail-API und importiert pro Zaehlpunkt die taeglichen `consumptionsurya/g` und `consumptionsurya/p` Messreihen. Daraus werden die gleichen Messgroessen wie im XLSX Import abgeleitet, inklusive synthetischer `TOTAL`-Reihen. EDA-Zeitstempel werden als `Europe/Vienna` interpretiert und UTC gespeichert.

Die bekannten EDA-Portal-Endpunkte, Response-Shapes und das Mapping der `g`/`p` Reihen sind in [EDA Portal API](eda-api.md) dokumentiert.

## Zaehlpunkt-Validierung

Zaehlpunkte muessen 33 Zeichen lang sein, mit `AT00` beginnen und duerfen keine eingebetteten Community-Codes wie `RC107893` enthalten.

Im lokalen Dev-Modus loggt der EDA Import die Zaehlpunkt-Erkennung und uebersprungene Kandidaten mit `LOG_LEVEL=debug`.
