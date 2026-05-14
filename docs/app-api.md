# App API

Diese Endpunkte gehoeren zur EEG-Sumsum-App, nicht direkt zur EDA-Portal-API.

## XLSX Upload

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

## EDA Import

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

## EDA Auto-Import

Wenn EDA konfiguriert ist, startet der Server zusaetzlich einen automatischen Import ueber `github.com/robfig/cron/v3`.

Standardmaessig laeuft er taeglich um 03:15 Uhr `Europe/Vienna` und importiert die letzten 30 abgeschlossenen Tage bis gestern 23:45. Die Administration zeigt den letzten automatischen Aufruf, dessen Ergebnis und den naechsten geplanten Lauf.
