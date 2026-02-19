#[global_allocator]
static GLOBAL: tikv_jemallocator::Jemalloc = tikv_jemallocator::Jemalloc;

use hermes_gateway::server;
use anyhow::Result;
use clap::Parser;
use std::path::PathBuf;

#[derive(Parser)]
#[command(name = "hermes-gateway", about = "High-performance API gateway data plane")]
struct Cli {
    /// Path to gateway config file
    #[arg(short, long, default_value = "config.toml")]
    config: PathBuf,

    /// Listen address
    #[arg(short, long, default_value = "0.0.0.0:8080")]
    listen: String,

    /// Admin API listen address (for health/metrics)
    #[arg(long, default_value = "0.0.0.0:9091")]
    admin_listen: String,
}

fn main() -> Result<()> {
    let cli = Cli::parse();

    let worker_threads = server::runtime::get_container_cpu_limit();

    let rt = tokio::runtime::Builder::new_multi_thread()
        .worker_threads(worker_threads)
        .enable_all()
        .build()?;

    rt.block_on(server::bootstrap::run(server::bootstrap::BootstrapArgs {
        config_path: cli.config,
        listen: cli.listen,
        admin_listen: cli.admin_listen,
    }))
}
