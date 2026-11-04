# gocsvify

[Русская версия](README_RU.md)

A daemon that converts **XLS, XLSX, XLSM, ACCDB and MDB** files to **CSV** (one file per sheet or table). It runs in two modes:

- **standalone**: watches the `in` folder, converts files and puts the results into `out`;
- **server**: the same, plus an HTTP API for uploading files, checking status and downloading results.

No external tools are required: Excel and Access formats are read by built-in parsers.

## Requirements

- Go 1.27+

## Build and run

```bash
go mod tidy
go build -o gocsvify .

# standalone mode ("startServer": false in the settings)
./gocsvify -settings_path=./settings.json

# server mode ("startServer": true in the settings)
./gocsvify -env=dev
```

On the first run without `-settings_path`, a `settings.json` with default values is created next to the executable.

| Flag | Default | Description |
|------|---------|-------------|
| `-env` | `prod` | `dev`: paths from the settings are used as is. `prod`: relative paths are resolved against the executable's directory |
| `-settings_path` | empty | Path to the settings file. If empty, `settings.json` next to the executable is used (created with defaults if missing) |

Stop with `Ctrl+C` or `SIGTERM`. The service finishes the files it is working on (up to 30 s); unfinished files stay in the input folder.

## Standalone mode

Put a file into the `in` folder. The result appears in `out/<file name without extension>/0.csv`, `1.csv`, … The source file is moved to `history`, or to `failed` if the conversion fails. If there are no files for longer than `idleTimeout`, the daemon exits.

## HTTP API

```bash
# upload: returns fileKey
curl -F "file=@report.xlsx" http://localhost:8080/upload

# status: new | processing | done | error
curl "http://localhost:8080/check?key=<fileKey>"

# download all sheets (ZIP)
curl -OJ "http://localhost:8080/download?key=<fileKey>"

# download a single sheet (zero-based)
curl -OJ "http://localhost:8080/download?key=<fileKey>&sheetNum=0"
```

Main status codes: `202` file accepted, `413` file larger than `maxUploadSize`, `422` file not processed yet or the conversion failed, `404` key not found. The API has no authentication, so put a reverse proxy in front of it.

## Settings (`settings.json`)

Missing keys fall back to the default values. Durations are either a Go duration string (`"10s"`, `"1m30s"`, `"2h"`) or a number of seconds.

| Key | Default | Description |
|-----|---------|-------------|
| `appDir` | `./gocsvify_data` | Working folder: `app.db` database, temporary files |
| `in` | `./gocsvify_data/in` | Incoming files |
| `out` | `./gocsvify_data/out` | Results (`out/<key>/N.csv`) |
| `history` | `./gocsvify_data/history` | Processed source files |
| `archives` | `./gocsvify_data/archives` | ZIP cache for `/download` |
| `failed` | `./gocsvify_data/failed` | Files that could not be converted |
| `isAbsPath` | `false` | `true`: paths are used as is. `false`: relative to the executable's directory (always as is with `-env=dev`) |
| `sleep` | `10s` | Pause between checks of the input folder |
| `idleTimeout` | `60s` | How long the daemon waits without files before stopping (`0`: never stop) |
| `lifeTime` | `60m` | How long to keep results, archives and temporary files |
| `lifeTimeHistory` | `60m` | How long to keep source files in `history` |
| `padRows` | `false` | Pad rows with empty cells up to the end of the range (xlsx) |
| `delimiter` | `;` | CSV delimiter (a single one-byte character) |
| `withHidden` | `false` | Convert hidden xlsx sheets |
| `startServer` | `false` | Enable the HTTP server |
| `serverPort` | `8080` | Server port |
| `readTimeout` | `600s` | Request read timeout |
| `readHeadTimeout` | `30s` | Request header read timeout |
| `writeTimeout` | `0` | Response write timeout (`0`: no limit) |
| `maxUploadSize` | `256` | Maximum upload size in MB (must be > 0) |
| `logLevel` | `info` | `debug`, `info`, `warn`, `error` |
| `logFile` | empty | Log file (empty: stdout), JSON format |
| `useMdbLib` | `false` | Read Access files through `mdbtools` instead of the built-in reader |

JSON keys are case-sensitive. See [`doc.md`](doc.md) for details on the whole project.

## Tests

```bash
go vet ./...
go test -race ./...
go test -short ./...   # skips the slowest tests
```

## License

MIT, see [`LICENSE`](LICENSE).

## Acknowledgements

Developed with the assistance of [Claude](https://claude.ai) by Anthropic.
