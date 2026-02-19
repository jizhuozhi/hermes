FROM golang:1.24-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /hermes-controller ./cmd/controller

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=builder /hermes-controller /usr/local/bin/hermes-controller
COPY config.yaml.example /etc/hermes/config.yaml
ENTRYPOINT ["hermes-controller", "-config", "/etc/hermes/config.yaml"]
