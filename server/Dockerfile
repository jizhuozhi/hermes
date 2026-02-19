FROM node:22-alpine AS frontend
WORKDIR /app/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.24-alpine AS backend
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=frontend /app/web/dist ./web/dist
RUN CGO_ENABLED=0 go build -o /hermes-server ./cmd/server

FROM alpine:3.21
RUN apk add --no-cache ca-certificates wget
COPY --from=backend /hermes-server /usr/local/bin/hermes-server
COPY config.yaml.example /etc/hermes/config.yaml
EXPOSE 9080
ENTRYPOINT ["hermes-server", "-config", "/etc/hermes/config.yaml"]
