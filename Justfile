default: build

set unstable := true
set dotenv-load := true

PROJECT := env('PROJECT')
REGION := env('REGION')

NAME := env('NAME', "cloudrun-primer")
REPO := env('REPO', "UNDEFINED")
TAG := env('TAG', datetime("%Y%m%d%H%M%S"))

CLOUD_BUILD_REPO := env('CLOUD_BUILD_REPO')

build:
    go build -o ./exe .

cloud-build:
    gcloud builds submit \
    --tag {{ CLOUD_BUILD_REPO }}/{{ NAME }}:{{ TAG }} \
    --project {{ PROJECT }}

build-amd64:
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w" -o ./exe .

docker-build:
    docker build -t {{ NAME }} .

docker-build-amd64:
    docker build --platform linux/amd64 -t {{ NAME }} .

docker-run:
    docker run --rm -it -p :8000:8000 {{ NAME }}

docker-tag-push:
    docker tag {{ NAME }}:latest {{ REPO }}/{{ NAME }}:{{ TAG }}
    docker push {{ REPO }}/{{ NAME }}:{{ TAG }}

docker-release: build-amd64 docker-build-amd64 docker-tag-push

promote:
    #!/bin/bash
    gcloud run deploy stubbed \
    --image={{ REPO }}/{{ NAME }}:{{ TAG }} \
    --allow-unauthenticated \
    --port=8000 \
    --min-instances=0 \
    --max-instances=1 \
    --memory=512Mi \
    --cpu=1 \
    --ingress=all \
    --execution-environment=gen2 \
    --region={{ REGION }} \
    --project={{ PROJECT }} \
    --set-env-vars=INITIAL=1

clean:
    rm -f ./exe

cloud-export:
    gcloud run services describe {{ NAME }} \
    --format export \
    --region {{ REGION }} \
    --project {{ PROJECT }} \
    >service.yaml

cloud-update:
    gcloud run services replace service.yaml --project {{ PROJECT }} 

cloud-certificates:
    gcloud beta compute --project {{ PROJECT }} ssl-certificates list

cloud-domain-mapping:
    gcloud beta run domain-mappings list \
    --project {{ PROJECT }} \
    --region {{ REGION }}
