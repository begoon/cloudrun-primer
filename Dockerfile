FROM golang:1.24-alpine AS builder

WORKDIR /
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags '-w -s' -o exe main.go

FROM scratch

COPY --from=builder /exe /

ENTRYPOINT ["/exe"]
