# llamatop

A terminal dashboard for [llama.cpp](https://github.com/ggml-org/llama.cpp)'s `llama-server` – like `top`, but for your LLM.

llamatop polls the server's own endpoints (`/slots`, `/props`, `/metrics`, …) and shows what is
going on inside: per-slot state and progress, live prefill and generation speed, KV cache usage,
completed requests with TTFT and duration, history charts, alerts, a benchmark and an AI-generated
assessment of the server's health.

```
 llamatop   ok   http://127.0.0.1:39789  23:12:19  every 1s  ping 0 ms
╭───────────────────────────────────────────╮╭─────────────────────────────────────────────────────╮
│ Model                                     ││ Throughput                                          │
│ File      sim-7B-Instruct-Q4_K_M.gguf     ││ Gen           44.8 t/s  ▁█                          │
│ Alias     sim-7b · Q4_K - Medium          ││ Prefill     1195.5 t/s  ▁█                          │
│ Size      7.2 B params · 4.4 GB           ││ Active       2/4 slots  █▆                          │
│ Context   16384 / slot · trained 32768    ││ Queued           0 req  ▁▁                          │
│ Slots     4                               ││                                                     │
│ LoRA      sim-style 1                     ││ KV        ██░░░░░░░░░░░░░░  13.3%  8746/65536       │
│ Build     sim                             ││                                                     │
╰───────────────────────────────────────────╯╰─────────────────────────────────────────────────────╯
╭──────────────────────────────────────────────────────────────────────────────────────────────────╮
│ Slots                                                                                            │
│ › #0  warm    ██░░░░░░░░░░░░░░░░   8.5%    1396/16384   task 9                                   │
│   #1  gen     █░░░░░░░░░░░░░░░░░   3.1%     513/16384   task 10        44.8 t/s  cache   0%      │
│               prefill ██████████████████ 453/453   gen ████████████████░░ 60/67                  │
│   #2  prefill ██████░░░░░░░░░░░░  32.0%    5241/16384   task 11      1195.5 pp/s  cache   0%     │
│               prefill ██████░░░░░░░░░░░░ 1656/5241   gen ░░░░░░░░░░░░░░░░░░ 0/235                │
│   #3  warm    ██░░░░░░░░░░░░░░░░   9.7%    1596/16384   task 7                                   │
╰──────────────────────────────────────────────────────────────────────────────────────────────────╯
```

*(Overview against the built-in simulator, `llamatop -sim`.)*

llamatop is deliberately llama.cpp-only: it relies on llama-server specifics such as `/slots`, so it
can show far more than the generic `/metrics` scraping of multi-backend tools.

## Features

- **Overview** – model, throughput with sparklines, KV cache, every slot with state
  (`idle`, `warm`, `prefill`, `gen`), prefill and generation progress, sampler parameters
- **History** – line chart with time axis for gen and prefill speed, active slots, KV usage,
  queue and ping, over 1 min to 1 h
- **Requests** – completed requests detected from slot changes, with TTFT, duration, gen speed,
  cache hits and the sampler parameters they ran with; statistics (avg, p50, p95, max) and
  processed server metrics
- **Tools** – slot actions (erase, save, restore), LoRA adapter scales, and a benchmark series
- **Analysis** – alerts with hold times, and an AI assessment of the monitoring data
- **Simulator** – `-sim` runs a built-in fake llama-server to try everything without a real one
- **Snapshots** – `-once` prints one screen or JSON for scripts
- **Configuration** – TOML config file plus command-line flags

## Requirements

- Go 1.24 or newer to build
- llama-server from llama.cpp; tested with build `b10540`

Some features need llama-server start flags:

| Flag | Needed for |
|---|---|
| `--metrics` | server metrics, queue display and queue alert |
| `--slot-save-path <dir>` | slot actions (erase, save, restore) |
| `--lora <file.gguf>` | LoRA adapters in the Tools view |

`/slots` is enabled by default; if it was turned off with `--no-slots`, most of llamatop stays empty.

## Install

Download the archive for your platform from the
[releases page](https://github.com/movray/llamatop/releases) (currently Linux amd64), then:

```sh
sha256sum -c llamatop-*-linux-amd64.tar.gz.sha256
tar -xzf llamatop-*-linux-amd64.tar.gz
./llamatop-*-linux-amd64/llamatop -version
```

The binary is statically linked and needs no further libraries.

Or build it yourself:

```sh
go build -o llamatop .
```

## Usage

```sh
./llamatop -u http://localhost:8080          # monitor a server
./llamatop -u http://gpu-box:8080 -i 500ms   # poll twice per second
./llamatop -sim                              # try it with the built-in simulator
./llamatop -once                             # print one screen and exit
./llamatop -once -output json                # snapshot as JSON
./llamatop -c my.toml                        # use a config file
./llamatop -print-config                     # show the effective configuration
```

For a server with an API key, use `-k <key>`, the environment variable `LLAMA_API_KEY`, or
`api_key` in the config file.

## Views and keys

Switch views with `1`–`5` or `Tab` / `Shift+Tab`.

| Key | Everywhere |
|---|---|
| `p` / `Space` | pause polling |
| `+` / `-` | longer / shorter poll interval (250 ms … 10 s) |
| `q` / `Esc` / `Ctrl+C` | quit |

| View | Keys |
|---|---|
| 1 Overview | `↑/↓` select slot · `Enter` sampler parameters · `e` erase · `s` save · `r` restore slot |
| 2 History | `←/→` choose metric · `z` / `Z` longer / shorter time range |
| 3 Requests | `↑/↓` select request · `Enter` details · `End` follow the latest |
| 4 Tools | `b` start benchmark · `x` abort · `↑/↓` adapter · `←/→` scale · `Enter` apply · `PgUp/PgDn` scroll results |
| 5 Analysis | `a` run AI analysis · `x` abort · `r` show the data sent · `↑/↓` `PgUp/PgDn` scroll |

`vim` keys `h j k l` work wherever arrows do. Erasing a slot asks `y/N`; only `y` confirms.

## How requests are measured

llama-server has no request log, so llamatop detects requests from changes of `id_task` in
`/slots` between two polls. Start, first token and end are therefore estimated to the middle of a
poll interval: **times are accurate to about ± one interval**. Use a shorter interval (`-i 250ms`)
for more precise TTFT values. Requests that start and finish between two polls are logged with
their prompt size only. Requests that were already running when llamatop started show `?` for TTFT
and duration and are left out of those statistics. The gen speed of a request is measured between
the first and the last poll that saw it generating, so it is exact whenever it was seen at least twice.

Live speeds come from token deltas between polls; the `/metrics` counters are only updated when a
task finishes. The averages `prompt_tokens_seconds` and `predicted_tokens_seconds` are reset by the
server on every `/metrics` request, so llamatop computes averages from the totals instead.

## Alerts

| Alert | Severity | Default condition | Default hold |
|---|---|---|---|
| server unreachable | CRIT | `/health` fails | 3 s |
| server health | WARN | health is not `ok` (e.g. loading) | 5 s |
| KV cache full | WARN / CRIT | ≥ 85 % / ≥ 95 % | 10 s |
| queue | WARN | requests deferred (needs `--metrics`) | 10 s |
| slow response | WARN | ping > 500 ms | 10 s |
| gen speed dropped | WARN | below 50 % of the usual speed per slot | 15 s |

An alert fires only after its condition has held for the hold time. The usual gen speed is learned
separately for each number of concurrently generating slots, because every slot gets slower when
several generate at once. Active alerts show in the header and above the key help in every view;
view 5 lists recent events.

## Benchmark

`b` in the Tools view runs four requests through `/completion`: a short (≈64 tokens) and a long
(≈2048 tokens) prompt, each once alone and once with as many parallel requests as there are slots.
Each request generates exactly 128 tokens with the prompt cache disabled. Prompt lengths, output
tokens and parallelism can be changed in the `[benchmark]` config section. Prefill and generation
speeds come from llama-server's own timings. Results of earlier runs stay listed for comparison.
The benchmark loads the server and its requests appear in the request log.

## AI analysis

`a` in view 5 sends a plain-text summary of the monitoring data – model, slots, the last 15 minutes
of history, request statistics, server counters, the last benchmark and alerts – to
`/v1/chat/completions` and shows the model's assessment. `r` shows exactly what was sent.

By default the monitored server answers; `-ai-url` points to another llama-server, for example one
running a larger model. Thinking is disabled for reasoning models (`chat_template_kwargs`), which
makes answers much faster; enable it with `-ai-thinking`. The analysis is a starting point, not a
diagnosis – small models in particular get details wrong. It only works with llama-server
(or servers that accept llama-server's request format), not with the OpenAI API itself.

## Configuration

Precedence: built-in defaults < config file < `LLAMA_API_KEY` < command-line flags.

The config file is TOML. llamatop reads the file given with `-c`; without `-c` it loads
`~/.config/llamatop/config.toml` (or `$XDG_CONFIG_HOME/llamatop/config.toml`) if it exists.
Unknown keys and invalid values are reported as errors.

```sh
mkdir -p ~/.config/llamatop
cp llamatop.example.toml ~/.config/llamatop/config.toml
chmod 600 ~/.config/llamatop/config.toml   # if you put an API key in it
```

[`llamatop.example.toml`](llamatop.example.toml) documents every key with its default. Sections:

| Section | Settings |
|---|---|
| `[server]` | `url`, `api_key`, `interval`, `timeout` |
| `[ai]` | `url`, `api_key`, `max_tokens`, `temperature`, `thinking` |
| `[ui]` | start `view`, start `range`, chart `fill`, `history` length, `log_size` |
| `[alerts]` | `enabled`, `kv_warn_pct`, `kv_crit_pct`, `ping`, `gen_drop` |
| `[alerts.hold]` | hold time per alert (file only) |
| `[benchmark]` | `output_tokens`, `short_prompt`, `long_prompt`, `parallel` |
| `[colors]` | `accent`, `green`, `yellow`, `red`, `cyan`, `dim`, `border` as `#RRGGBB` or 0–255 (file only) |

All other settings also have a flag; see `llamatop -h`. `-print-config` prints the effective
configuration with API keys masked. The built-in default URL is `http://localhost:8080`, the
default port of llama-server; set your own in the config file or with `-u`.

## JSON snapshot

`-once` polls twice, one interval apart (rates need two samples), then prints:

```sh
./llamatop -once -output json | jq '.throughput'
```

```json
{
  "gen_tps": 44.88819043041916,
  "prefill_tps": 1197.0184114778442,
  "active_slots": 2,
  "kv_used": 8746,
  "kv_total": 65536,
  "kv_pct": 13.3453369140625
}
```

The full object contains `time`, `url`, `health`, `ping_ms`, `model`, `throughput`, `slots`,
`metrics`, `lora` and active `alerts`.

## Simulator

`-sim` starts a fake llama-server on a free local port and monitors it. It serves the same
endpoints as the real server – including slot actions, LoRA, `/completion` and chat completions –
with 4 slots, varying load that sometimes overloads the server and queues requests, prompt cache
hits, and every 5 minutes a 30-second slowdown that triggers the gen-speed alert. It pre-runs one
minute of load so there is something to see right away.

## Releases

Pushing a version tag starts the [release workflow](.github/workflows/release.yml): it runs
`go vet` and the tests, builds a static binary for Linux amd64 with the version built in
(`llamatop -version`), and publishes a GitHub release with the archive, a SHA-256 checksum and
generated release notes.

```sh
git tag v0.1.0
git push origin v0.1.0
```

## Development

```sh
go test ./...   # ~8 s; includes an end-to-end run against the simulator
go vet ./...
```

The tests check request detection, alerts, JSON output, the configuration, and render all five
views at 80×24, 120×40 and 200×60 to make sure nothing overflows the terminal.

| File | Contents |
|---|---|
| `main.go` | startup |
| `config.go` | configuration file and flags |
| `api.go` | HTTP client and llama-server types |
| `monitor.go` | time series, live rates, request detection |
| `stats.go`, `alerts.go` | statistics and alert rules |
| `actions.go`, `bench.go`, `analysis.go` | slot/LoRA actions, benchmark, AI analysis |
| `ui.go` | Bubble Tea model and keys |
| `view_*.go`, `widgets.go` | views, panels, charts |
| `output.go` | JSON snapshot |
| `sim.go` | simulator |

Built with [Bubble Tea](https://github.com/charmbracelet/bubbletea),
[Lip Gloss](https://github.com/charmbracelet/lipgloss) and
[BurntSushi/toml](https://github.com/BurntSushi/toml).
