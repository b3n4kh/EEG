# Deployment

## Podman Compose

Lege eine `.env` Datei an:

```sh
SESSION_SECRET=replace-with-a-long-random-secret
ADMIN_USERNAME=admin
ADMIN_PASSWORD=replace-with-a-strong-password
ADMIN_API_TOKEN=replace-with-a-random-upload-token
PORT=8080
```

Wenn EDA Import genutzt wird, zusaetzlich:

```sh
EDA_USERNAME=user@example.com
EDA_PASSWORD=replace-with-eda-password
EDA_COMMUNITY_ID=replace-with-community-id
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

## Lokal bauen und starten

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

## Release Image aus GHCR

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
