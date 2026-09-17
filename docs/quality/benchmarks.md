# Benchmarks

Prizm currently has a minimal vector-index benchmark suite:

```bash
go test ./internal/vector -run '^$' -bench . -benchmem -count=5
```

It measures HNSW insertion, HNSW search, bulk insertion, and brute-force
search. It does not measure end-to-end lifecycle latency, NATS throughput,
SQLite contention, provider latency, or scheduler reliability.

## Measured baseline

The 2026-07-11 exploratory baseline used Windows/amd64, Go 1.26.4, and an Intel
Core i7-14700HX. It used `-count=1`, so these values document scale and are not
regression thresholds.

| Benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| HNSW insert | 457,088 | 124,725 | 6 |
| HNSW search | 41,855 | 30,784 | 180 |
| HNSW bulk insert | 3,661,524 | 210,520 | 388 |
| Brute-force search | 55,645 | 40,200 | 189 |

For comparisons, preserve raw `-benchmem -count=5` output and use the same
hardware, power mode, Go version, dataset seed, and process load. Add lifecycle
and persistence benchmarks only after deterministic fixtures exist.
