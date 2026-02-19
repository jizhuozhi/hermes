.PHONY: build build-release run clean test fmt clippy

build:
	cargo build --manifest-path Cargo.toml

build-release:
	cargo build --manifest-path Cargo.toml --release

run:
	cargo run --manifest-path Cargo.toml -- -c config.toml -l 0.0.0.0:8080

test:
	cargo test --manifest-path Cargo.toml

fmt:
	cargo fmt --manifest-path Cargo.toml

clippy:
	cargo clippy --manifest-path Cargo.toml -- -D warnings

clean:
	cargo clean --manifest-path Cargo.toml
