# GCP Cloud Run primer

[![Run on Google Cloud](https://deploy.cloud.run/button.svg)](https://deploy.cloud.run?git_repo=https://github.com/begoon/cloudrun-primer)

This Go application can be used as a stub image for newly created Cloud Run services.

## Routes

| Route | Description |
| --- | --- |
| `/` | Prints environment variables, CPU count, and available GCP metadata, including project, zone, and service-account email. Also displays IAP identity information when supplied. |
| `/speed` | Downloads Hetzner's [100 MiB test file](https://fsn1-speed.hetzner.com/100MB.bin) and reports the URL, progress, throughput, and elapsed time. |
| `/ip` | Returns the service's public outbound IP address using ipify. The lookup has a 10-second timeout; upstream failures return HTTP 502. |
| `/fs/` | Serves files and directory listings from the container filesystem. |
| `/ls/` | Lists files with sizes and modification times. Append a path, such as `/ls/proc/`. |

The speed test streams the download through a counter rather than storing the file. It uses `curl/8.7.1` as its user agent because the test host closed connections using the previous user agent. Output uses decimal MB, so the 100 MiB file appears as approximately 105 MB:

```text
url=https://fsn1-speed.hetzner.com/100MB.bin
started at ...
block 52 MB/52 MB | throughput 65 MB/s | elapsed 801.897557ms
downloaded 105 MB | throughput 63 MB/s | elapsed 1.662485033s
```

The download has a two-minute timeout and is canceled when the caller disconnects. Connection failures and upstream non-200 responses return HTTP 502 before streaming starts. If a download fails after output has started, the response includes an `error=download failed: ...` line instead of a success summary; its HTTP status remains 200 because the headers have already been sent.

## Run locally

Use Go 1.27 or later:

```sh
go run .
```

The server listens on port `8000` by default and uses `PORT` when set. Cloud Run supplies this environment variable.

```sh
curl http://localhost:8000/ip
curl http://localhost:8000/speed
go test ./...
```

## Build, push, and deploy

The Dockerfile builds a static Go binary and copies it into a `scratch` image. Set the project, service region, and Artifact Registry repository for your environment:

```sh
export PROJECT=home-az
export REGION=europe-west1
export NAME=cloudrun-primer
export REPO=europe-west2-docker.pkg.dev/home-az/az
export TAG=$(date +%Y%m%d%H%M%S)

gcloud auth configure-docker europe-west2-docker.pkg.dev

docker buildx build --platform linux/amd64 --provenance=false \
  -t "$REPO/$NAME:$TAG" --push .

gcloud run deploy "$NAME" \
  --image="$REPO/$NAME:$TAG" \
  --region="$REGION" \
  --project="$PROJECT"
```

The single-platform build disables provenance so the registry receives one image manifest instead of an image index, an image manifest, and a provenance attestation.

The `Justfile` also provides build and registry commands and loads variables from `.env`:

```sh
just build
just docker-build-amd64
just docker-tag-push
```

`just docker-release` builds and pushes the Linux/amd64 image. `just promote` deploys the configured `NAME` with the recipe's demo settings. Set an explicit `TAG` when invoking release and promote separately so both commands use the same image tag.

This application is for educational purposes. Its routes expose environment variables and container files; restrict access when those contain sensitive information.
