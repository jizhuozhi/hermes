FROM rust:1.93-alpine AS builder
RUN apk add --no-cache build-base
WORKDIR /app
COPY Cargo.toml Cargo.lock ./
RUN mkdir src && echo "fn main() {}" > src/main.rs
RUN cargo build --release || true
RUN rm -rf src target/release/hermes-gateway target/release/deps/hermes_gateway-*

COPY src/ src/
RUN cargo build --release

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=builder /app/target/release/hermes-gateway /usr/local/bin/hermes-gateway
COPY config.toml.example /etc/hermes/config.toml
EXPOSE 8080 9091
ENTRYPOINT ["hermes-gateway", "-c", "/etc/hermes/config.toml", "-l", "0.0.0.0:8080"]
