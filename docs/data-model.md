# Datenmodell

## Tabellen

| Tabelle | Zweck |
| --- | --- |
| `users` | Admin- und Teilnehmerkonten |
| `metering_points` | Zaehlpunkte |
| `user_metering_points` | Zuordnung Account zu Zaehlpunkten |
| `api_tokens` | Gehashte Upload-Tokens |
| `import_batches` | Importierte Dateien und Zeitraeume |
| `measurements` | Viertelstundenwerte |
| `overview_summaries` | Summen aus dem XLSX Sheet `Übersicht` oder EDA Import |
| `scheduled_import_status` | Status des automatischen EDA Imports |

Messwerte sind eindeutig ueber:

```text
metering_point_id + direction + metric_key + interval_start
```

## Sichtbarkeit

- Administratoren sehen alle Zaehlpunkte und synthetische `TOTAL`-Reihen.
- Teilnehmer sehen nur explizit zugewiesene Zaehlpunkte.
- Teilnehmer duerfen `TOTAL`-Daten nicht sehen.

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

## Importregeln

- XLSX-Uploads muessen idempotent sein.
- Gleiche Datei oder ueberlappende Zeitraeume duerfen keine Duplikate erzeugen.
- Timestamps aus XLSX werden als `Europe/Vienna` geparst und UTC gespeichert.
- Der EDA Import speichert ebenfalls UTC-Zeitstempel.
