FROM golang:1.27-alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags '-w -s' -o /exe .

FROM scratch

COPY --from=builder /exe /

ENTRYPOINT ["/exe"]
