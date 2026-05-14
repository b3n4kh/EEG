# EDA Portal API Notes

Diese Notizen dokumentieren die EDA-Portal-Endpunkte, die EEG Sumsum verwendet oder historisch verwendet hat. Die EDA API ist keine stabile oeffentliche Schnittstelle; bei 404/401/Shape-Aenderungen zuerst diese Datei, `internal/eda/client.go` und `cmd/eda-verify` abgleichen.

## Base URLs

Es gibt zwei relevante Hosts:

| Zweck | Default | ENV |
| --- | --- | --- |
| Login und Energy-Community-Metadaten | `https://prod-api.eda-portal.at/api` | `EDA_BASE_URL` |
| Einzelne Zaehlpunkt-Messreihen | `https://prod.eda-portal.at/api` | `EDA_SERIES_BASE_URL` |

Wichtig: `consumptionsurya/...` liegt im aktuellen Portal nicht unter `prod-api.eda-portal.at/api`. Wenn diese Route dort aufgerufen wird, kommt ein 404 wie `The route api/consumptionsurya/... could not be found`.

## Auth

### `POST {EDA_BASE_URL}/v4/auth/login`

Request:

```json
{
  "email": "user@example.com",
  "password": "secret"
}
```

Bekannte Response-Formen:

```json
{
  "token": "<jwt>"
}
```

oder:

```json
{
  "data": {
    "token": "<jwt>"
  }
}
```

Alle weiteren EDA Requests senden:

```text
Authorization: Bearer <jwt>
Accept: application/json
Content-Type: application/json
```

## Energy Community Details

### `GET {EDA_BASE_URL}/pwa/energycommunities/<CommunityID>`

Diese Route liefert die Metadaten der Energy Community. EEG Sumsum nutzt daraus vor allem die Zaehlpunkte und Teilnehmerdaten.

Bekannte relevante Response-Felder:

```json
{
  "data": {
    "meteringPoints": [
      {
        "meteringPointId": "AT0010000000000000001000000000001",
        "energyDirection": "CONSUMPTION",
        "isGenerator": false,
        "gridOperator": "AT001000",
        "networkOperator": "AT001000",
        "participant": {
          "id": "participant-1",
          "firstName": "Petra",
          "lastName": "Akhras",
          "address": {
            "street": "Summergasse",
            "street_number": "3",
            "zip": "3400",
            "city": "Kierling",
            "country": "AT"
          }
        }
      }
    ]
  }
}
```

Bekannte Varianten:

- Manche Responses verwenden `meterpoints` statt `meteringPoints`.
- Die Richtung kann aus `energyDirection`, `isGenerator` oder Feldnamen im Roh-JSON abgeleitet werden.
- Teilnehmerdaten koennen fehlen. Dann werden keine Teilnehmerkonten fuer diesen Zaehlpunkt synchronisiert.

Validierung im Import:

- Zaehlpunkt-ID muss 33 Zeichen lang sein.
- Zaehlpunkt-ID muss mit `AT00` beginnen.
- Werte mit eingebetteten Community-Codes wie `RC107893` oder `GC123456` werden ignoriert.

## Einzelne Zaehlpunkt-Messreihen

Diese Routen kommen aus dem aktuellen EDA-Portal-Frontend und verwenden den separaten `EDA_SERIES_BASE_URL`.

### `POST {EDA_SERIES_BASE_URL}/consumptionsurya/g/<meterID>`

Liefert die `g`-Zeitreihe fuer einen Zaehlpunkt.

### `POST {EDA_SERIES_BASE_URL}/consumptionsurya/p/<meterID>`

Liefert die `p`-Zeitreihe fuer einen Zaehlpunkt.

Request fuer beide Routen:

```json
{
  "time": {
    "in": {
      "min": "2026-05-01T00:00",
      "max": "2026-05-13T23:45"
    }
  },
  "groupBy": "day",
  "energyCommunityId": "BOyL2dgERgjlzw5R",
  "name": "AT0010000000000000001000000000001"
}
```

Bekannte Response-Form:

```json
{
  "success": true,
  "s": true,
  "data": [
    ["2026-05-01T00:00:00", 12.34],
    ["2026-05-02T00:00:00", 10.5]
  ],
  "meta": {
    "scale_x": "day"
  }
}
```

Interpretation:

- Zeitstempel werden als `Europe/Vienna` interpretiert und UTC gespeichert.
- `groupBy` ist aktuell `day`; andere Gruppierungen importiert EEG Sumsum nicht.
- `meta.scale_x` sollte `day` sein. Andere Werte werden geloggt.
- `success=false` oder `s=false` ist ein Importfehler.

## Mapping der `g`/`p` Reihen

EDA liefert fuer jeden Zaehlpunkt zwei Reihen. Welche Messgroessen daraus entstehen, haengt von der Richtung des Zaehlpunkts ab.

### Verbrauchszaehlpunkt (`CONSUMPTION`)

| EDA Reihe | Ziel-Metrik |
| --- | --- |
| `g` | `Gesamtverbrauch lt. Messung [KWH]` |
| `g` | `Verbrauch lt. Messung entsprechend dem Teilnahmefaktor je ZP und EC-ID [KWH]` |
| `p` | `Anteil aus der Gemeinschaft [KWH]` |
| `min(g, p)` | `Eigendeckung gemeinschaftliche Erzeugung [KWH]` |
| `min(g, p)` | `Eigendeckung aus erneuerbarer Energie [KWH]` |

### Erzeugungszaehlpunkt (`GENERATION`)

| EDA Reihe | Ziel-Metrik |
| --- | --- |
| `g` | `Gesamte gemeinschaftliche Erzeugung [KWH]` |
| `g` | `Erzeugung lt. Messung entsprechend dem Teilnahmefaktor und EC-ID [KWH]` |
| `p` | `Restueberschuss bei EG und je ZP [KWH]` |

Danach erzeugt EEG Sumsum synthetische `TOTAL`-Reihen fuer Admin-Auswertungen. Teilnehmer duerfen `TOTAL` nicht sehen.

## Historische aggregierte Community-Routen

Die erste EDA-Integration hat aggregierte Community-Endpunkte verwendet:

### `POST {EDA_BASE_URL}/pwa/energycommunities/<CommunityID>/kpiData`

Bekannte Response-Felder:

```json
{
  "success": true,
  "data": {
    "autarky": 27.8,
    "ownConsumption": 34.5,
    "community": 107.4,
    "feed": 203.5,
    "remainingDemand": 279.0,
    "communityGrouped": [
      {
        "enixiGenerationType": "Photovoltaik",
        "sum": 107.4
      }
    ]
  }
}
```

### `POST {EDA_BASE_URL}/pwa/energycommunities/<CommunityID>/meterdata`

Bekannte Response-Felder:

```json
{
  "success": true,
  "s": true,
  "data": {
    "substitutesOrMissingData": false,
    "sumGeneration": 12.5,
    "sumFeed": 7.0,
    "generationSeries": [
      {
        "date": "2026-05-01T00:00:00",
        "value": 12.5,
        "methods": "L1"
      }
    ],
    "feedSeries": [
      {
        "date": "2026-05-01T00:00:00",
        "value": 7.0,
        "methods": null
      }
    ]
  }
}
```

Diese Routen liefern Community-Aggregate, aber keine sauber getrennten Teilnehmer-Zaehlpunktdaten. Der aktuelle Import verwendet deshalb die Detailroute plus `consumptionsurya/g` und `consumptionsurya/p`.

## Hilfswerkzeug

`cmd/eda-verify` kann Login, Discovery und historische `kpiData`/`meterdata` Shapes pruefen:

```sh
EDA_USERNAME='user@example.com' \
EDA_PASSWORD='secret' \
EDA_COMMUNITY_ID='BOyL2dgERgjlzw5R' \
go run ./cmd/eda-verify -discover -raw
```

Fuer Debugging des aktuellen Imports sind zusaetzlich die Serverlogs relevant. Bei `LOG_LEVEL=debug` werden akzeptierte und uebersprungene Zaehlpunkt-Kandidaten geloggt.
