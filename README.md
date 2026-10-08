# llamatop

A terminal dashboard for [llama.cpp](https://github.com/ggml-org/llama.cpp)'s `llama-server` – like `top`, but for your LLM.

llamatop polls the server's own endpoints (`/slots`, `/props`, `/metrics`, …) and shows what is
going on inside: per-slot state and progress, live prefill and generation speed, KV cache usage,
completed requests with TTFT and duration, history charts, alerts, a benchmark and an AI-generated
assessment of the server's health.

![llamatop overview: four slots generating in parallel, request log and statistics](https://raw.githubusercontent.com/movray/llamatop/main/docs/llamatop.png)

*Overview of a llama-server with four slots under load.*

llamatop is deliberately llama.cpp-only: it relies on llama-server specifics such as `/slots`, so it
can show far more than the generic `/metrics` scraping of multi-backend tools.

## Features

- **Overview** – model, throughput with sparklines, KV cache, every slot with state
  (`idle`, `warm`, `prefill`, `gen`), prefill and generation progress, sampler parameters;
  slot actions: erase a slot's KV cache, save it to a file, restore it
- **History** – line chart with time axis for gen and prefill speed, active slots, KV usage,
  queue and ping, over 1 min to 1 h
- **Requests** – completed requests detected from slot changes, with TTFT, duration, gen speed,
  cache hits and the sampler parameters they ran with; statistics (avg, p50, p95, max) and
  processed server metrics
- **Tools** – LoRA adapter scales and a benchmark series
- **Analysis** – alerts with hold times, and an AI assessment of the monitoring data
- **Models** – router mode: all models with their status, monitor any of them or follow the loaded
  one, load and unload models
- **Simulator** – `-sim` runs a built-in fake llama-server to try everything without a real one,
  `-sim-router` one in router mode
- **Snapshots** – `-once` prints one screen or JSON for scripts
- **Configuration** – TOML config file plus command-line flags

## Requirements

- Go 1.24 or newer to build
- llama-server from llama.cpp; tested with build `b10540`, router mode with `b11514`

Some features need llama-server start flags:

| Flag | Needed for |
|---|---|
| `--metrics` | server metrics, queue display and queue alert |
| `--slot-save-path <dir>` | slot actions (erase, save, restore) |
| `--lora <file.gguf>` | LoRA adapters in the Tools view |

`/slots` is enabled by default; if it was turned off with `--no-slots`, most of llamatop stays empty.

## Install

Download the archive for your platform from the
[releases page](https://github.com/movray/llamatop/releases):

| Platform | Archive |
|---|---|
| Linux x86-64 | `llamatop-<version>-linux-amd64.tar.gz` |
| Linux ARM64 (e.g. Raspberry Pi 4/5, Graviton) | `llamatop-<version>-linux-arm64.tar.gz` |
| macOS Apple Silicon (M1 and later) | `llamatop-<version>-darwin-arm64.tar.gz` |
| macOS Intel | `llamatop-<version>-darwin-amd64.tar.gz` |

```sh
sha256sum -c llamatop-*-linux-amd64.tar.gz.sha256      # macOS: shasum -a 256 -c …
tar -xzf llamatop-*-linux-amd64.tar.gz
./llamatop-*-linux-amd64/llamatop -version
```

The Linux binaries are statically linked; the macOS binaries only use the system library. Neither needs
anything else installed.

**macOS:** the binaries are not signed by Apple. If the archive was downloaded with a browser,
macOS refuses to start the binary ("cannot be opened"). Remove the quarantine flag once:

```sh
xattr -d com.apple.quarantine llamatop-*-darwin-*/llamatop
```

Downloads with `curl` are not affected.

Or build it yourself:

```sh
go build -o llamatop .
```

## Usage

```sh
./llamatop -u http://localhost:8080          # monitor a server
./llamatop -u http://gpu-box:8080 -i 500ms   # poll twice per second
./llamatop -m qwen3.6-35b                    # router mode: monitor this model
./llamatop -sim                              # try it with the built-in simulator
./llamatop -once                             # print one screen and exit
./llamatop -once -output json                # snapshot as JSON
./llamatop -c my.toml                        # use a config file
./llamatop -print-config                     # show the effective configuration
```

For a server with an API key, use `-k <key>`, the environment variable `LLAMA_API_KEY`, or
`api_key` in the config file.

## Views and keys

Switch views with `1`–`6` or `Tab` / `Shift+Tab`.

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
| 6 Models | `↑/↓` select model · `Enter` monitor it · `f` follow the loaded model · `l` load · `u` unload |

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

## Router mode

A llama-server started without a model (`-m`) runs as a **router**: it starts a separate
instance per model on demand – from the cache, `--models-dir` or `--models-preset` – and forwards
each request by its model name. llamatop recognises this from `/props` (`"role": "router"`) and
then always monitors one model at a time: slots, metrics, history, requests and alerts belong to
that model, while the header shows its name and status.

| How the model is chosen | |
|---|---|
| default | follow the loaded model: the one that is loaded, sleeping or loading; when the router replaces it, llamatop switches along |
| `-m <name>` / `server.model` | always this model (pinned), also while it is not loaded |
| view 6, `Enter` / `f` | pin the selected model / follow the loaded one again |

Each model keeps its own history and request log while llamatop runs, so switching back and forth
loses nothing but the time in between.

Monitoring never loads a model: all its requests carry `autoload=false`, so the router answers
"model is not loaded" instead of starting one. While a model is sleeping (`--sleep-idle-seconds`)
llamatop does not poll its `/slots` and `/lora-adapters` either, because those would wake it up;
`/props` and `/metrics` don't. View 6 lists all models with status, context size and parallel
slots from their launch arguments, file size, source and the full launch arguments of the
selected model. `l` and `u` load and unload a model (`POST /models/load`, `/models/unload`); if
the router already runs `--models-max` models, loading asks first, since the router then unloads
the least recently used one.

Slot actions and the benchmark go to the monitored model. Setting LoRA scales is not possible
through the router: it routes POST requests by the `"model"` field of a JSON object, and
`/lora-adapters` takes a list. The AI analysis is answered by the monitored model, or by the model
given with `-ai-model` / `ai.model` – unlike monitoring, the analysis may load that model. With
`--models-max 1` this unloads the monitored model.

## Slot actions

In the Overview, select a slot with `↑/↓` and press:

| Key | Action | llama-server call |
|---|---|---|
| `e` | **erase** – discard the slot's KV cache (its cached context) | `POST /slots/{id}?action=erase` |
| `s` | **save** – write the slot's KV cache to a file | `POST /slots/{id}?action=save` |
| `r` | **restore** – load a saved KV cache into the slot | `POST /slots/{id}?action=restore` |

Erasing asks `Erase slot N? y/N`; only `y` confirms, `Enter` cancels. If the slot is busy, the
prompt warns about it. Save and restore ask for a file name (default `slot<id>.bin`, then the
last name used for that slot); the name is relative to the server's `--slot-save-path`
directory, so the file ends up on the server, not on the machine running llamatop. The result
shows as a status line, e.g. `Slot 1 erased, 2183 tokens discarded`.

All three need llama-server to be started with `--slot-save-path <dir>` – without it the server
rejects even erasing. Erasing frees KV cache space; the next request on that slot has to process
its full prompt again.

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
`~/.config/llamatop/config.toml` (or `$XDG_CONFIG_HOME/llamatop/config.toml`) if it exists –
on macOS as well.
Unknown keys and invalid values are reported as errors.

```sh
mkdir -p ~/.config/llamatop
cp llamatop.example.toml ~/.config/llamatop/config.toml
chmod 600 ~/.config/llamatop/config.toml   # if you put an API key in it
```

[`llamatop.example.toml`](llamatop.example.toml) documents every key with its default. Sections:

| Section | Settings |
|---|---|
| `[server]` | `url`, `api_key`, `model`, `interval`, `timeout` |
| `[ai]` | `url`, `api_key`, `model`, `max_tokens`, `temperature`, `thinking` |
| `[ui]` | start `view`, start `range`, chart `fill`, `history` length, `log_size` |
| `[alerts]` | `enabled`, `kv_warn_pct`, `kv_crit_pct`, `ping`, `gen_drop` |
| `[alerts.hold]` | hold time per alert (file only) |
| `[benchmark]` | `output_tokens`, `short_prompt`, `long_prompt`, `parallel` |
| `[colors]` | `accent`, `green`, `yellow`, `red`, `cyan`, `dim`, `border` as `#RRGGBB` or 0–255 (file only) |

All other settings also have a flag; see `llamatop -h`. `-print-config` prints the effective
configuration with API keys masked. The built-in default URL is `http://localhost:8080`, the
default port of llama-server; set your own in the config file or with `-u`.

## JSON snapshot

`-once` polls twice, one interval apart (rates need two samples), then prints – against a router
one or two more polls first, to recognise it and pick the model:

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
`metrics`, `lora` and active `alerts`; in router mode also `router` with the monitored model and
the status of all models.

## Simulator

`-sim` starts a fake llama-server on a free local port and monitors it. It serves the same
endpoints as the real server – including slot actions, LoRA, `/completion` and chat completions –
with 4 slots, varying load that sometimes overloads the server and queues requests, prompt cache
hits, and every 5 minutes a 30-second slowdown that triggers the gen-speed alert. It pre-runs one
minute of load so there is something to see right away.

`-sim-router` simulates a router with three models: `sim-7b` (4 slots) is loaded at start,
`sim-32b` (2 slots, slower) and `sim-coder-1.5b` (2 slots, fast) are not. At most two models run
at once; a load takes 3 seconds, and loading a third model unloads the least recently used.

## Releases

Pushing a version tag starts the [release workflow](.github/workflows/release.yml): it runs
`go vet` and the tests on Linux amd64, Linux arm64 and macOS, builds static binaries for
Linux amd64/arm64 and macOS amd64/arm64 with the version built in (`llamatop -version`), and
publishes a GitHub release with the archives, SHA-256 checksums and generated release notes.

```sh
git tag v0.1.0
git push origin v0.1.0
```

## Development

```sh
go test ./...   # ~14 s; includes end-to-end runs against both simulators
go vet ./...
```

The tests check request detection, alerts, JSON output, the configuration, and render all six
views at 80×24, 120×40 and 200×60 (and in router mode) to make sure nothing overflows the terminal.

| File | Contents |
|---|---|
| `main.go` | startup |
| `config.go` | configuration file and flags |
| `api.go` | HTTP client and llama-server types |
| `monitor.go` | time series, live rates, request detection |
| `stats.go`, `alerts.go` | statistics and alert rules |
| `actions.go`, `bench.go`, `analysis.go` | slot/LoRA actions, benchmark, AI analysis |
| `ui.go` | Bubble Tea model and keys |
| `view_*.go`, `widgets.go` | views, panels, charts (`view_models.go`: router models) |
| `output.go` | JSON snapshot |
| `sim.go` | simulator |

Built with [Bubble Tea](https://github.com/charmbracelet/bubbletea),
[Lip Gloss](https://github.com/charmbracelet/lipgloss) and
[BurntSushi/toml](https://github.com/BurntSushi/toml).

## License

llamatop is released under the [MIT License](LICENSE).

The release binaries include the Go runtime and third-party modules under the MIT and BSD
licenses; their license texts are in `THIRD_PARTY_LICENSES.txt` in every release archive.
`scripts/third-party-licenses.sh` generates that file for the current `GOOS`/`GOARCH`. Some
distribution packages of Go (e.g. Debian's) lack the Go license file; the script then warns and
notes the gap, or uses the file given in `GO_LICENSE`. The release workflow runs it with
`LICENSES_STRICT=1`, where any missing license text is an error.
