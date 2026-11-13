//! Fits spherical `e-means` (CPU, and Metal when available) and the 25-iteration Lloyd baseline
//! over a file of unit vectors exported by Vani, from the same Forgy initial centroids at each
//! `(k, seed)`, and writes the cluster assignments and centroids beside a JSON summary on stdout.
//!
//! This is the process contract Vani's offline strata experiment (`docs/CLUSTER_STUDY.md` in the
//! Vani repository) uses: no binding, plain files.
//!
//! ```text
//! cargo run --release -p ojas-cluster --example vani_cluster -- \
//!     --vectors pooled.vectors --out fits/ --k 8,16,32 --seeds 5 > fits/summary.json
//! ```
//!
//! Input, `pooled.vectors` (little-endian):
//!
//! ```text
//! magic "VANIPVEC" (8) | version u32 = 1 | n u32 | dim u32 | keys_sha256 (32) | n*dim f32, unit rows
//! ```
//!
//! Output, per fit, beside the summary:
//!
//! ```text
//! <algo>.k<K>.s<seed>.assign  "VANIASGN" | version u32 = 1 | n u32 | k u32 | keys_sha256 (32) | n u32 labels
//! <algo>.k<K>.s<seed>.cent    "VANICENT" | version u32 = 1 | k u32 | dim u32 | k*dim f32
//! ```
//!
//! The labels written are not the engine's own: they are the nearest centroid of the final
//! centroids, computed here in `f64` the same way for every algorithm, and the reported MSE is the
//! mean squared Euclidean distance to that centroid (the spherical objective, with each centroid
//! normalised). The engines' own labels are compared and the mismatch count reported.

#![forbid(unsafe_code)]

use std::fmt::Write as _;
use std::io::Read;
use std::path::{Path, PathBuf};
use std::time::Instant;

use ojas_cluster::{
    lloyd_fit, ClusterDevice, CpuMath, EmeansConfig, EmeansEngine, LloydConfig, MetalPrecision,
    SplitMix64,
};
use ojas_core::{Budget, Tensor};

const VECTORS_MAGIC: &[u8; 8] = b"VANIPVEC";
const ASSIGN_MAGIC: &[u8; 8] = b"VANIASGN";
const CENT_MAGIC: &[u8; 8] = b"VANICENT";
const FORMAT_VERSION: u32 = 1;
const HEADER_LEN: usize = 8 + 4 + 4 + 4 + 32;
const MAX_ROWS: u64 = 200_000;
const MAX_DIM: u64 = 8192;
const MAX_ROW_BYTES: u64 = 512 << 20;
const UNIT_TOLERANCE: f64 = 1e-3;
const MAX_SEEDS: u64 = 32;
const MAX_ROUNDS: usize = 50;
const MAX_K_LIST: usize = 64;
const BUDGET_BYTES: u64 = 2 * 1024 * 1024 * 1024;
const INIT_SEED_XOR: u64 = 0x494e_4954_4345_4e54;
const EVAL_WORKERS: usize = 8;

/// The rows of a vectors file.
struct Vectors {
    n: usize,
    dim: usize,
    keys_sha256: [u8; 32],
    rows: Vec<f32>,
}

/// Parse and validate a vectors file image: exact size for its header, bounded header, finite
/// unit-length rows (a zero row refuses).
fn parse_vectors(raw: &[u8]) -> Result<Vectors, String> {
    if raw.len() < HEADER_LEN {
        return Err(format!(
            "file is {} bytes, shorter than its {HEADER_LEN} byte header",
            raw.len()
        ));
    }
    if &raw[..8] != VECTORS_MAGIC {
        return Err("not a VANIPVEC file".to_string());
    }
    let u32_at = |o: usize| u32::from_le_bytes([raw[o], raw[o + 1], raw[o + 2], raw[o + 3]]);
    let version = u32_at(8);
    if version != FORMAT_VERSION {
        return Err(format!("version {version}, this reads {FORMAT_VERSION}"));
    }
    let n = u64::from(u32_at(12));
    let dim = u64::from(u32_at(16));
    if n == 0 || n > MAX_ROWS {
        return Err(format!("{n} rows (bound 1..={MAX_ROWS})"));
    }
    if dim == 0 || dim > MAX_DIM {
        return Err(format!("width {dim} (bound 1..={MAX_DIM})"));
    }
    let row_bytes = n * dim * 4;
    if row_bytes > MAX_ROW_BYTES {
        return Err(format!("{row_bytes} bytes of rows, over {MAX_ROW_BYTES}"));
    }
    let want = HEADER_LEN as u64 + row_bytes;
    if raw.len() as u64 != want {
        return Err(format!(
            "file is {} bytes, its header implies {want}",
            raw.len()
        ));
    }
    let mut keys_sha256 = [0_u8; 32];
    keys_sha256.copy_from_slice(&raw[20..52]);
    let (n, dim) = (n as usize, dim as usize);
    let mut rows = Vec::with_capacity(n * dim);
    for (i, chunk) in raw[HEADER_LEN..].as_chunks::<4>().0.iter().enumerate() {
        let v = f32::from_le_bytes(*chunk);
        if !v.is_finite() {
            return Err(format!("row {} holds a non-finite value", i / dim));
        }
        rows.push(v);
    }
    for (r, row) in rows.chunks_exact(dim).enumerate() {
        let norm = row
            .iter()
            .map(|&v| f64::from(v) * f64::from(v))
            .sum::<f64>()
            .sqrt();
        if (norm - 1.0).abs() > UNIT_TOLERANCE {
            return Err(format!("row {r} has length {norm}, want 1"));
        }
    }
    Ok(Vectors {
        n,
        dim,
        keys_sha256,
        rows,
    })
}

fn read_bounded(path: &Path) -> Result<Vec<u8>, String> {
    let meta = std::fs::metadata(path).map_err(|e| format!("{}: {e}", path.display()))?;
    if !meta.is_file() {
        return Err(format!("{} is not a regular file", path.display()));
    }
    let limit = HEADER_LEN as u64 + MAX_ROW_BYTES;
    if meta.len() > limit {
        return Err(format!(
            "{} is {} bytes, over {limit}",
            path.display(),
            meta.len()
        ));
    }
    let mut raw = Vec::with_capacity(meta.len() as usize);
    std::fs::File::open(path)
        .and_then(|f| f.take(limit + 1).read_to_end(&mut raw))
        .map_err(|e| format!("{}: {e}", path.display()))?;
    Ok(raw)
}

fn encode_assignments(keys_sha256: &[u8; 32], k: usize, labels: &[u32]) -> Vec<u8> {
    let mut b = Vec::with_capacity(HEADER_LEN + 4 * labels.len());
    b.extend_from_slice(ASSIGN_MAGIC);
    b.extend_from_slice(&FORMAT_VERSION.to_le_bytes());
    b.extend_from_slice(&(labels.len() as u32).to_le_bytes());
    b.extend_from_slice(&(k as u32).to_le_bytes());
    b.extend_from_slice(keys_sha256);
    for l in labels {
        b.extend_from_slice(&l.to_le_bytes());
    }
    b
}

fn encode_centroids(k: usize, dim: usize, centroids: &[f32]) -> Vec<u8> {
    let mut b = Vec::with_capacity(20 + 4 * centroids.len());
    b.extend_from_slice(CENT_MAGIC);
    b.extend_from_slice(&FORMAT_VERSION.to_le_bytes());
    b.extend_from_slice(&(k as u32).to_le_bytes());
    b.extend_from_slice(&(dim as u32).to_le_bytes());
    for v in centroids {
        b.extend_from_slice(&v.to_le_bytes());
    }
    b
}

/// `k` distinct rows chosen uniformly (Forgy), deterministic in `(seed, k)`.
fn forgy_init(rows: &[f32], n: usize, dim: usize, k: usize, seed: u64) -> Vec<f32> {
    let mut idx: Vec<u32> = (0..n as u32).collect();
    let mut rng = SplitMix64::new(SplitMix64::mix(seed ^ INIT_SEED_XOR ^ ((k as u64) << 32)));
    for i in 0..k {
        let rem = (n - i) as u64;
        let pick = i + rng.next_bounded_u64(rem) as usize;
        idx.swap(i, pick);
    }
    let mut out = Vec::with_capacity(k * dim);
    for &r in &idx[..k] {
        out.extend_from_slice(&rows[r as usize * dim..(r as usize + 1) * dim]);
    }
    out
}

/// Nearest centroid of each row by cosine to the normalised centroids (a zero centroid is never
/// nearest), ties to the lowest index, and the mean squared Euclidean distance to it, all in `f64`.
fn assign_f64(rows: &[f32], centroids: &[f32], n: usize, dim: usize, k: usize) -> (Vec<u32>, f64) {
    let mut unit = vec![0.0_f64; k * dim];
    let mut live = vec![false; k];
    for j in 0..k {
        let c = &centroids[j * dim..(j + 1) * dim];
        let norm = c
            .iter()
            .map(|&v| f64::from(v) * f64::from(v))
            .sum::<f64>()
            .sqrt();
        if norm > 0.0 && norm.is_finite() {
            live[j] = true;
            for (dst, &v) in unit[j * dim..(j + 1) * dim].iter_mut().zip(c) {
                *dst = f64::from(v) / norm;
            }
        }
    }
    let workers = EVAL_WORKERS.min(n).max(1);
    let per = n.div_ceil(workers);
    let parts: Vec<(Vec<u32>, f64)> = std::thread::scope(|scope| {
        let (unit, live) = (&unit, &live);
        let handles: Vec<_> = (0..workers)
            .filter(|w| w * per < n)
            .map(|w| {
                let (lo, hi) = (w * per, ((w + 1) * per).min(n));
                scope.spawn(move || {
                    let mut labels = Vec::with_capacity(hi - lo);
                    let mut total = 0.0_f64;
                    for r in lo..hi {
                        let x = &rows[r * dim..(r + 1) * dim];
                        let xx: f64 = x.iter().map(|&v| f64::from(v) * f64::from(v)).sum();
                        let mut best = f64::NEG_INFINITY;
                        let mut arg = 0_u32;
                        for j in 0..k {
                            if !live[j] {
                                continue;
                            }
                            let dot: f64 = x
                                .iter()
                                .zip(&unit[j * dim..(j + 1) * dim])
                                .map(|(&a, &b)| f64::from(a) * b)
                                .sum();
                            if dot > best {
                                best = dot;
                                arg = j as u32;
                            }
                        }
                        labels.push(arg);
                        total += xx + 1.0 - 2.0 * best;
                    }
                    (labels, total)
                })
            })
            .collect();
        handles
            .into_iter()
            .map(|h| h.join().expect("assignment worker panicked"))
            .collect()
    });
    let mut labels = Vec::with_capacity(n);
    let mut total = 0.0_f64;
    for (l, t) in parts {
        labels.extend(l);
        total += t;
    }
    (labels, total)
}

fn parse_k_list(s: &str, n: usize) -> Result<Vec<usize>, String> {
    let mut out = Vec::new();
    for part in s.split(',') {
        let k: usize = part
            .trim()
            .parse()
            .map_err(|_| format!("--k: {part:?} is not a whole number"))?;
        if k == 0 || k > n {
            return Err(format!("--k: {k} is outside 1..={n} (the row count)"));
        }
        if !out.contains(&k) {
            out.push(k);
        }
    }
    if out.is_empty() || out.len() > MAX_K_LIST {
        return Err(format!("--k: want 1..={MAX_K_LIST} values"));
    }
    out.sort_unstable();
    Ok(out)
}

fn hex(bytes: &[u8]) -> String {
    let mut s = String::with_capacity(bytes.len() * 2);
    for b in bytes {
        let _ = write!(s, "{b:02x}");
    }
    s
}

/// A JSON string literal.
fn json_str(s: &str) -> String {
    let mut o = String::with_capacity(s.len() + 2);
    o.push('"');
    for c in s.chars() {
        match c {
            '"' => o.push_str("\\\""),
            '\\' => o.push_str("\\\\"),
            '\n' => o.push_str("\\n"),
            '\r' => o.push_str("\\r"),
            '\t' => o.push_str("\\t"),
            c if (c as u32) < 0x20 => {
                let _ = write!(o, "\\u{:04x}", c as u32);
            }
            c => o.push(c),
        }
    }
    o.push('"');
    o
}

/// A finite `f64` as JSON; anything else is an error (the summary never carries a stand-in).
fn json_num(v: f64, what: &str) -> Result<String, String> {
    if v.is_finite() {
        Ok(format!("{v}"))
    } else {
        Err(format!("{what} is not finite ({v})"))
    }
}

struct Args {
    vectors: PathBuf,
    out: PathBuf,
    ks: Vec<usize>,
    seeds: u64,
    seed_base: u64,
    rounds: usize,
    nepochs: f64,
    lloyd_iters: usize,
    metal: bool,
}

fn parse_args(args: &[String], n: usize) -> Result<Args, String> {
    let mut a = Args {
        vectors: PathBuf::new(),
        out: PathBuf::new(),
        ks: Vec::new(),
        seeds: 3,
        seed_base: 1,
        rounds: 5,
        nepochs: 2.6,
        lloyd_iters: 25,
        metal: true,
    };
    let mut k_arg: Option<String> = None;
    let mut i = 0;
    while i < args.len() {
        let flag = args[i].as_str();
        let mut value = || -> Result<&str, String> {
            i += 1;
            args.get(i)
                .map(String::as_str)
                .ok_or_else(|| format!("{flag} wants a value"))
        };
        match flag {
            "--vectors" => a.vectors = PathBuf::from(value()?),
            "--out" => a.out = PathBuf::from(value()?),
            "--k" => k_arg = Some(value()?.to_string()),
            "--seeds" => a.seeds = value()?.parse().map_err(|_| "--seeds: not a number")?,
            "--seed-base" => {
                a.seed_base = value()?.parse().map_err(|_| "--seed-base: not a number")?
            }
            "--rounds" => a.rounds = value()?.parse().map_err(|_| "--rounds: not a number")?,
            "--nepochs" => a.nepochs = value()?.parse().map_err(|_| "--nepochs: not a number")?,
            "--lloyd-iters" => {
                a.lloyd_iters = value()?
                    .parse()
                    .map_err(|_| "--lloyd-iters: not a number")?
            }
            "--no-metal" => a.metal = false,
            other => return Err(format!("unknown argument {other:?}")),
        }
        i += 1;
    }
    if a.vectors.as_os_str().is_empty() || a.out.as_os_str().is_empty() {
        return Err("--vectors and --out are required".to_string());
    }
    if a.seeds == 0 || a.seeds > MAX_SEEDS {
        return Err(format!("--seeds must be within 1..={MAX_SEEDS}"));
    }
    if a.rounds == 0 || a.rounds > MAX_ROUNDS {
        return Err(format!("--rounds must be within 1..={MAX_ROUNDS}"));
    }
    if !(a.nepochs.is_finite() && a.nepochs > 0.0 && a.nepochs <= 100.0) {
        return Err("--nepochs must be within (0, 100]".to_string());
    }
    if a.lloyd_iters == 0 || a.lloyd_iters > 1000 {
        return Err("--lloyd-iters must be within 1..=1000".to_string());
    }
    a.ks = parse_k_list(k_arg.as_deref().ok_or("--k is required (comma list)")?, n)?;
    Ok(a)
}

fn median(xs: &[f64]) -> f64 {
    let mut s = xs.to_vec();
    s.sort_by(f64::total_cmp);
    let m = s.len() / 2;
    if s.len() % 2 == 1 {
        s[m]
    } else {
        0.5 * (s[m - 1] + s[m])
    }
}

#[derive(Clone, Copy, PartialEq, Eq)]
enum Algo {
    EmeansCpu,
    EmeansMetal,
    Lloyd,
}

impl Algo {
    fn name(self) -> &'static str {
        match self {
            Algo::EmeansCpu => "emeans_cpu",
            Algo::EmeansMetal => "emeans_metal",
            Algo::Lloyd => "lloyd",
        }
    }
}

/// One fit's result in the form every algorithm shares.
struct Fitted {
    centroids: Vec<f32>,
    engine_labels: Vec<u32>,
}

fn run_summary_fail<T>(r: Result<T, ojas_core::OjasError>, what: &str) -> Result<T, String> {
    r.map_err(|e| format!("{what}: {e}"))
}

fn run() -> Result<(), String> {
    let args: Vec<String> = std::env::args().skip(1).collect();
    // Two passes: --vectors is needed before --k can be bounded by n.
    let vectors_path = args
        .iter()
        .position(|a| a == "--vectors")
        .and_then(|i| args.get(i + 1))
        .ok_or("--vectors is required")?;
    let data = parse_vectors(&read_bounded(Path::new(vectors_path))?)
        .map_err(|e| format!("{vectors_path}: {e}"))?;
    let a = parse_args(&args, data.n)?;
    std::fs::create_dir_all(&a.out).map_err(|e| format!("{}: {e}", a.out.display()))?;

    let (n, dim) = (data.n, data.dim);
    let budget = Budget::new(BUDGET_BYTES);
    let x = run_summary_fail(
        Tensor::from_f32(&data.rows, &[n, dim], &budget),
        "load rows",
    )?;
    let cpu = run_summary_fail(
        EmeansEngine::new(
            ClusterDevice::Cpu {
                threads: None,
                math: CpuMath::Fast,
            },
            &budget,
        ),
        "cpu engine",
    )?;
    let (metal, metal_status) = if a.metal {
        match EmeansEngine::new(
            ClusterDevice::Metal {
                precision: MetalPrecision::F32,
            },
            &budget,
        ) {
            Ok(e) => (Some(e), ("ok".to_string(), String::new())),
            Err(e) => (None, ("not_run".to_string(), e.to_string())),
        }
    } else {
        (
            None,
            ("not_run".to_string(), "--no-metal was given".to_string()),
        )
    };
    eprintln!(
        "vani_cluster: n={n} dim={dim} k={:?} seeds={} rounds={} metal={}",
        a.ks, a.seeds, a.rounds, metal_status.0
    );

    let mut algos = vec![Algo::EmeansCpu];
    if metal.is_some() {
        algos.push(Algo::EmeansMetal);
    }
    algos.push(Algo::Lloyd);

    let mut runs_json: Vec<String> = Vec::new();
    for &k in &a.ks {
        for s in 0..a.seeds {
            let seed = a.seed_base + s;
            let init_rows = forgy_init(&data.rows, n, dim, k, seed);
            let init = run_summary_fail(Tensor::from_f32(&init_rows, &[k, dim], &budget), "init")?;
            let mut ecfg = EmeansConfig::new(k);
            ecfg.nepochs = a.nepochs;
            ecfg.seed = seed;
            ecfg.spherical = true;
            ecfg.capture_assignments = true;
            let mut lcfg = LloydConfig::new(k);
            lcfg.max_iters = a.lloyd_iters;
            lcfg.tol = 0.0;
            lcfg.spherical = true;
            lcfg.seed = seed;

            let fit_once = |algo: Algo| -> Result<Fitted, String> {
                match algo {
                    Algo::EmeansCpu | Algo::EmeansMetal => {
                        let engine = if algo == Algo::EmeansCpu {
                            &cpu
                        } else {
                            metal.as_ref().ok_or("metal engine missing")?
                        };
                        let f =
                            run_summary_fail(engine.fit_with_init(&x, &init, &ecfg), algo.name())?;
                        let c = run_summary_fail(f.centroids.f32_slice(), "centroids")?.to_vec();
                        Ok(Fitted {
                            centroids: c,
                            engine_labels: f.labels,
                        })
                    }
                    Algo::Lloyd => {
                        let f =
                            run_summary_fail(lloyd_fit(&x, Some(&init), &lcfg, &budget), "lloyd")?;
                        let c = run_summary_fail(f.centroids.f32_slice(), "centroids")?.to_vec();
                        Ok(Fitted {
                            centroids: c,
                            engine_labels: f.labels,
                        })
                    }
                }
            };

            // Untimed warmup per contender, then cyclic-interleaved timed rounds.
            let mut warm: Vec<Fitted> = Vec::new();
            for &algo in &algos {
                warm.push(fit_once(algo)?);
            }
            let mut times: Vec<Vec<f64>> = vec![Vec::new(); algos.len()];
            let mut identical = vec![true; algos.len()];
            for round in 0..a.rounds {
                for off in 0..algos.len() {
                    let c = (round + off) % algos.len();
                    let t0 = Instant::now();
                    let f = fit_once(algos[c])?;
                    times[c].push(t0.elapsed().as_secs_f64() * 1_000.0);
                    let same = f
                        .centroids
                        .iter()
                        .zip(&warm[c].centroids)
                        .all(|(p, q)| p.to_bits() == q.to_bits());
                    identical[c] &= same && f.centroids.len() == warm[c].centroids.len();
                }
            }

            for (c, &algo) in algos.iter().enumerate() {
                let f = &warm[c];
                let (labels, inertia) = assign_f64(&data.rows, &f.centroids, n, dim, k);
                let mut counts = vec![0_u64; k];
                for &l in &labels {
                    counts[l as usize] += 1;
                }
                let non_empty = counts.iter().filter(|&&c| c > 0).count();
                let mismatches = if f.engine_labels.len() == labels.len() {
                    labels
                        .iter()
                        .zip(&f.engine_labels)
                        .filter(|(p, q)| p != q)
                        .count()
                } else {
                    labels.len()
                };
                let stem = format!("{}.k{k}.s{seed}", algo.name());
                let assign_name = format!("{stem}.assign");
                let cent_name = format!("{stem}.cent");
                std::fs::write(
                    a.out.join(&assign_name),
                    encode_assignments(&data.keys_sha256, k, &labels),
                )
                .map_err(|e| format!("{assign_name}: {e}"))?;
                std::fs::write(
                    a.out.join(&cent_name),
                    encode_centroids(k, dim, &f.centroids),
                )
                .map_err(|e| format!("{cent_name}: {e}"))?;
                let ms: Vec<String> = times[c]
                    .iter()
                    .map(|&t| json_num(t, "fit time"))
                    .collect::<Result<_, _>>()?;
                let mse = inertia / n as f64;
                eprintln!(
                    "  {} k={k} seed={seed}: mse={mse:.6} non_empty={non_empty} engine_label_mismatches={mismatches} fit_ms median={:.2} min={:.2}",
                    algo.name(),
                    median(&times[c]),
                    times[c].iter().copied().fold(f64::INFINITY, f64::min),
                );
                runs_json.push(format!(
                    "{{\"algo\":{},\"k\":{k},\"seed\":{seed},\"assign\":{},\"centroids\":{},\"inertia_f64\":{},\"mse_f64\":{},\"non_empty\":{non_empty},\"labels_match_engine\":{},\"engine_label_mismatches\":{mismatches},\"repeat_identical\":{},\"fit_ms\":[{}]}}",
                    json_str(algo.name()),
                    json_str(&assign_name),
                    json_str(&cent_name),
                    json_num(inertia, "inertia")?,
                    json_num(mse, "mse")?,
                    mismatches == 0,
                    identical[c],
                    ms.join(","),
                ));
            }
        }
    }

    let summary = format!(
        "{{\"format\":\"vani-cluster-fit/1\",\"vectors\":{},\"n\":{n},\"dim\":{dim},\"keys_sha256\":\"{}\",\"spherical\":true,\"emeans_nepochs\":{},\"lloyd_iters\":{},\"timing_rounds\":{},\"init\":\"forgy\",\"metal\":{{\"status\":{},\"detail\":{}}},\"runs\":[{}]}}",
        json_str(&a.vectors.display().to_string()),
        hex(&data.keys_sha256),
        json_num(a.nepochs, "nepochs")?,
        a.lloyd_iters,
        a.rounds,
        json_str(&metal_status.0),
        json_str(&metal_status.1),
        runs_json.join(","),
    );
    println!("{summary}");
    drop(metal);
    drop(cpu);
    Ok(())
}

fn main() {
    if let Err(e) = run() {
        eprintln!("vani_cluster: {e}");
        std::process::exit(1);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn file(n: u32, dim: u32, rows: &[f32]) -> Vec<u8> {
        let mut b = Vec::new();
        b.extend_from_slice(VECTORS_MAGIC);
        b.extend_from_slice(&FORMAT_VERSION.to_le_bytes());
        b.extend_from_slice(&n.to_le_bytes());
        b.extend_from_slice(&dim.to_le_bytes());
        b.extend_from_slice(&[7_u8; 32]);
        for v in rows {
            b.extend_from_slice(&v.to_le_bytes());
        }
        b
    }

    fn good() -> Vec<u8> {
        file(2, 2, &[1.0, 0.0, 0.0, 1.0])
    }

    #[test]
    fn parses_a_well_formed_file() {
        let v = parse_vectors(&good()).expect("good file");
        assert_eq!((v.n, v.dim), (2, 2));
        assert_eq!(v.rows, vec![1.0, 0.0, 0.0, 1.0]);
        assert_eq!(v.keys_sha256, [7_u8; 32]);
    }

    #[test]
    fn refuses_every_malformed_file() {
        let mut bad_magic = good();
        bad_magic[0] = b'X';
        let mut bad_version = good();
        bad_version[8] = 9;
        let mut truncated = good();
        truncated.truncate(truncated.len() - 1);
        let mut trailing = good();
        trailing.push(0);
        let cases: Vec<(&str, Vec<u8>)> = vec![
            ("empty", Vec::new()),
            ("shorter than the header", good()[..40].to_vec()),
            ("bad magic", bad_magic),
            ("bad version", bad_version),
            ("truncated rows", truncated),
            ("trailing bytes", trailing),
            ("zero rows", file(0, 2, &[])),
            ("zero width", file(2, 0, &[])),
            ("rows over the bound", file(200_001, 1, &[])),
            ("width over the bound", file(1, 8193, &[])),
            ("n times dim over the byte bound", file(200_000, 8192, &[])),
            (
                "header claims more rows than the file holds",
                file(4_000_000_000, 2, &[1.0, 0.0]),
            ),
            ("NaN", file(1, 2, &[f32::NAN, 0.0])),
            ("infinity", file(1, 2, &[f32::INFINITY, 0.0])),
            ("zero row", file(1, 2, &[0.0, 0.0])),
            ("not unit length", file(1, 2, &[2.0, 0.0])),
        ];
        for (name, raw) in cases {
            assert!(parse_vectors(&raw).is_err(), "{name} was accepted");
        }
    }

    #[test]
    fn assignments_and_centroids_have_the_documented_layout() {
        let sha = [9_u8; 32];
        let a = encode_assignments(&sha, 3, &[2, 0, 1]);
        assert_eq!(&a[..8], b"VANIASGN");
        assert_eq!(u32::from_le_bytes(a[8..12].try_into().unwrap()), 1);
        assert_eq!(u32::from_le_bytes(a[12..16].try_into().unwrap()), 3); // n
        assert_eq!(u32::from_le_bytes(a[16..20].try_into().unwrap()), 3); // k
        assert_eq!(&a[20..52], &sha);
        assert_eq!(a.len(), 52 + 12);
        assert_eq!(u32::from_le_bytes(a[52..56].try_into().unwrap()), 2);
        let c = encode_centroids(2, 3, &[1.0, 2.0, 3.0, 4.0, 5.0, 6.0]);
        assert_eq!(&c[..8], b"VANICENT");
        assert_eq!(u32::from_le_bytes(c[12..16].try_into().unwrap()), 2);
        assert_eq!(u32::from_le_bytes(c[16..20].try_into().unwrap()), 3);
        assert_eq!(c.len(), 20 + 24);
        assert_eq!(f32::from_le_bytes(c[20..24].try_into().unwrap()), 1.0);
    }

    #[test]
    fn forgy_picks_distinct_rows_deterministically() {
        let n = 20;
        let rows: Vec<f32> = (0..n).flat_map(|i| [i as f32, 0.0]).collect();
        let a = forgy_init(&rows, n, 2, 8, 1);
        assert_eq!(a, forgy_init(&rows, n, 2, 8, 1));
        assert_ne!(a, forgy_init(&rows, n, 2, 8, 2));
        let mut firsts: Vec<i32> = a.chunks(2).map(|r| r[0] as i32).collect();
        firsts.sort_unstable();
        firsts.dedup();
        assert_eq!(firsts.len(), 8, "a row was chosen twice");
        // k = n takes every row.
        let all = forgy_init(&rows, n, 2, n, 3);
        let mut all_firsts: Vec<i32> = all.chunks(2).map(|r| r[0] as i32).collect();
        all_firsts.sort_unstable();
        assert_eq!(all_firsts, (0..n as i32).collect::<Vec<_>>());
    }

    #[test]
    fn assignment_is_the_nearest_normalised_centroid_with_ties_to_the_lowest() {
        let rows = [1.0_f32, 0.0, 0.0, 1.0, 0.6, 0.8];
        // Centroid 1 is not unit length: it is normalised before it is compared.
        let cents = [1.0_f32, 0.0, 0.0, 5.0];
        let (labels, inertia) = assign_f64(&rows, &cents, 3, 2, 2);
        assert_eq!(labels, vec![0, 1, 1]);
        // Row 2 = (.6,.8) against (0,1): |x-c|^2 = 2 - 2*.8 = .4; the others are exact.
        // The rows are f32, so 0.6 and 0.8 are not exact: a 1e-6 tolerance.
        assert!((inertia - 0.4).abs() < 1e-6, "inertia {inertia}");
        // Identical centroids tie to the lowest index.
        let tie = [0.0_f32, 1.0, 0.0, 1.0];
        let (l, _) = assign_f64(&rows, &tie, 3, 2, 2);
        assert_eq!(l, vec![0, 0, 0]);
        // A zero centroid is never nearest.
        let dead = [0.0_f32, 0.0, 1.0, 0.0];
        let (l, _) = assign_f64(&rows, &dead, 3, 2, 2);
        assert!(l.iter().all(|&x| x == 1), "labels {l:?}");
    }

    #[test]
    fn k_list_and_args_are_bounded() {
        assert_eq!(parse_k_list("16, 8,8", 100).unwrap(), vec![8, 16]);
        for bad in ["", "0", "101", "x", "8,,16", "-1", "1.5"] {
            assert!(parse_k_list(bad, 100).is_err(), "{bad:?} was accepted");
        }
        let many = (1..=65)
            .map(|k| k.to_string())
            .collect::<Vec<_>>()
            .join(",");
        assert!(parse_k_list(&many, 100).is_err());
        let ok: Vec<String> = ["--vectors", "v", "--out", "o", "--k", "4"]
            .iter()
            .map(|s| s.to_string())
            .collect();
        assert!(parse_args(&ok, 10).is_ok());
        for extra in [
            vec!["--seeds", "0"],
            vec!["--seeds", "33"],
            vec!["--rounds", "0"],
            vec!["--nepochs", "NaN"],
            vec!["--nepochs", "0"],
            vec!["--lloyd-iters", "0"],
            vec!["--bogus"],
            vec!["--seeds"],
        ] {
            let mut a = ok.clone();
            a.extend(extra.iter().map(|s| s.to_string()));
            assert!(parse_args(&a, 10).is_err(), "{extra:?} was accepted");
        }
        assert!(
            parse_args(&ok[..4].to_vec(), 10).is_err(),
            "--k is required"
        );
    }

    #[test]
    fn json_helpers_escape_and_refuse_non_finite() {
        assert_eq!(json_str("a\"b\\c\n\u{1}"), "\"a\\\"b\\\\c\\n\\u0001\"");
        assert!(json_num(f64::NAN, "x").is_err());
        assert!(json_num(f64::INFINITY, "x").is_err());
        assert_eq!(json_num(0.25, "x").unwrap(), "0.25");
        assert_eq!(hex(&[0, 255, 16]), "00ff10");
        assert_eq!(median(&[3.0, 1.0, 2.0]), 2.0);
        assert_eq!(median(&[4.0, 1.0, 2.0, 3.0]), 2.5);
    }
}
