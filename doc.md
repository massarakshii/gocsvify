# gocsvify documentation

[Русская версия](doc_ru.md)

## Overview

**gocsvify** is a service that converts Excel (XLS, XLSX, XLSM) and Access (ACCDB, MDB) files to CSV. It runs as a daemon: it watches the input folder, converts files with a worker pool and puts the results into the output folder. It can optionally start an HTTP API for uploading files, checking status and downloading results.

License: MIT, see [`LICENSE`](LICENSE)

### Features
- **Formats**: XLS, XLSX, XLSM, ACCDB, MDB
- **Output**: CSV, one file per sheet/table, UTF-8
- **Parallel processing**: worker pool (one worker per CPU), 5-minute timeout per task
- **Two modes**: standalone (daemon and folders only) and server (daemon + HTTP API)
- **Resilience**: a panic while processing one file does not kill the worker; a broken file goes to `failed` and the rest keep being processed
- **Graceful shutdown**: on SIGINT/SIGTERM the daemon waits for running tasks to finish (up to 30 s)
- **JSON configuration**: paths, timeouts, upload limit, delimiter, etc.

## Architecture

```
┌─────────────┐     ┌──────────────┐     ┌─────────────────────┐
│  HTTP API   │────▶│ Job (daemon) │────▶│ Converters          │
│  /upload    │     │ worker pool  │     │ xls / xlsx / accdb  │
└─────────────┘     └──────────────┘     └─────────────────────┘
       │                    │
       │                    ▼
       │          ┌──────────────────┐
       └─────────▶│ BoltDB (Message) │
                  └──────────────────┘
```

### Components

#### 1. HTTP API (`server/server.go`)
- `POST /upload`: upload a file
- `GET /check?key=...`: conversion status
- `GET /download?key=...[&sheetNum=N]`: download a CSV sheet or a ZIP with all sheets

`StartServer` returns an `*http.Server` and an error channel. A startup error (for example, the port is busy) is delivered through the channel; `os.Exit` is never called. Server timeouts come from the settings.

#### 2. Job (`converters/job.go`)
Main `Run` loop:
1. Creates the working directories.
2. If the input folder contains files (subfolders and hidden `.xxx` files are ignored), starts the worker pool. Otherwise it removes expired data from `out`, `archives`, `tmp` (by `lifeTime`) and `history` (by `lifeTimeHistory`).
3. If there were no files for longer than `idleTimeout`, it stops (in server mode the next upload starts the daemon again).
4. Waits `sleep` between iterations and exits when the context is canceled.

File processing (`convertOne`):
- the converter is chosen by extension (case-insensitive);
- the result is written to the temporary folder `tmp/<uuid>` and then renamed to `out/<key>`; a previous result with the same key is replaced only after a successful conversion;
- the source file is moved to `history`; on error, to `failed`;
- message status: `new` → `processing` → `done` / `error`; when the daemon is stopped, the task goes back to `new`.

#### 3. Daemon Manager (`daemon/manager.go`)
- `StartIfNotRunning(job)` starts the daemon if it is not running and returns a channel that is closed when the daemon finishes;
- `Stop(timeout)` cancels the context and waits for the daemon to finish for at most `timeout`.

#### 4. Converters (`converters/xls`, `converters/xlsx`, `converters/accdb`)
They implement the `Converter { Convert(csvPath string) error }` interface.

| Format | Implementation |
|--------|----------------|
| XLSX/XLSM | XML archive parser; sheet order follows `workbook.xml`; hidden sheets are skipped (unless `withHidden` is on); without `workbook.xml`, sheets are ordered by the number in the file name (`sheet2` before `sheet10`); dates handle the 1900 leap-year bug and the 1904 date system |
| XLS | own OLE2/BIFF8 parser (`libs/xlslib`) |
| ACCDB/MDB | own Jet/ACE reader (`libs/accdbread`); with `useMdbLib: true` and `mdbtools` installed, `mdb-export`/`mdb-tables` are used |

#### 5. Message store (`store/`, `converters/MessageStore.go`)
File metadata (key, original name, status, message) is stored in BoltDB (`<appDir>/app.db`, bucket `Message`).
- server mode: the key is a UUID;
- standalone mode: the key is the file name without extension, and the original name is kept in the message (so a result can be matched to its source).

#### 6. Settings (`settings/settings.go`)
Read from JSON on top of default values (a missing key is not zeroed), then validated. If the file does not exist, a `settings.json` with default values is created next to the executable on first run.

## Project layout

```
gocsvify/
├── main.go                  # Entry point, lifecycle, signals
├── converters/
│   ├── interface.go         # Converter interface
│   ├── job.go               # Daemon loop, worker pool
│   ├── MessageStore.go      # BoltDB-backed store
│   ├── xls/                 # XLS converter (testdata/sample.xls)
│   ├── xlsx/                # XLSX/XLSM converter
│   └── accdb/               # ACCDB/MDB converter (testdata/*.accdb)
├── daemon/
│   ├── job.go               # Job interface
│   └── manager.go           # Daemon start/stop
├── server/server.go         # HTTP API
├── store/                   # Message and the MessageStore interface
├── settings/settings.go     # Settings
├── zipper/                  # Packing results into ZIP
├── libs/
│   ├── xlslib/              # BIFF8 (XLS) parser
│   ├── accdbread/           # Jet/ACE (ACCDB/MDB) reader
│   ├── configTime/          # Duration for JSON ("10s" or seconds)
│   ├── dirs/                # Directory creation
│   └── str/                 # UUID, numeric string check
├── settings.json
└── <appDir>/                # Working data (in/out/history/archives/failed/tmp, app.db)
```

## Installation and running

### Requirements
- **Go 1.27+** (uses `encoding/json/v2` and `errors.AsType`)
- No external tools needed. `mdbtools` (`mdb-export`, `mdb-tables`) is optional and only used with `useMdbLib: true`

### Build
```bash
go mod tidy
go build -o gocsvify .
```

### Flags
| Flag | Default | Description |
|------|---------|-------------|
| `-env` | `prod` | `dev`: paths from the settings are used as is; `prod`: relative paths are resolved against the executable's directory |
| `-settings_path` | empty | Path to the settings file; if empty, `settings.json` next to the executable is used (created with defaults if missing) |

### Examples
```bash
# Standalone mode (startServer: false in the settings)
./gocsvify -settings_path=./settings.json

# Server mode (startServer: true in the settings)
./gocsvify -env=dev
```
The HTTP server is enabled only by the `startServer` setting.

> The default settings file lives next to the executable. If the binary is installed in a read-only directory (for example `/usr/local/bin`), or you use `go run` (which builds into a temporary directory), pass `-settings_path` explicitly.

### Shutdown behavior
- SIGINT/SIGTERM: the server shuts down gracefully (up to 5 s), then the daemon is canceled and finishes its current files (up to 30 s). Unfinished files return to status `new` and stay in the input folder.
- Standalone mode: if there are no files for longer than `idleTimeout`, the daemon finishes and the process exits.
- Server error (port busy, etc.): the daemon is stopped and the process exits with an error.

## Configuration (`settings.json`)

JSON does not support comments; the example below is plain JSON.

```json
{
  "appDir": "./gocsvify_data",
  "in": "./gocsvify_data/in",
  "out": "./gocsvify_data/out",
  "history": "./gocsvify_data/history",
  "archives": "./gocsvify_data/archives",
  "failed": "./gocsvify_data/failed",
  "isAbsPath": false,
  "sleep": "10s",
  "idleTimeout": "60s",
  "lifeTime": "60m",
  "lifeTimeHistory": "60m",
  "padRows": false,
  "delimiter": ";",
  "withHidden": false,
  "startServer": true,
  "serverPort": 8080,
  "writeTimeout": 0,
  "readTimeout": "600s",
  "readHeadTimeout": "30s",
  "maxUploadSize": 256,
  "logLevel": "info",
  "logFile": "",
  "useMdbLib": false
}
```

| Key | Description |
|-----|-------------|
| `appDir` | Application working directory: `app.db` database, `tmp/` temporary files |
| `in`, `out`, `history`, `archives`, `failed` | Folders: incoming files; results (`out/<key>/N.csv`); processed sources; ZIP cache for `/download`; files that failed |
| `isAbsPath` | `true`: paths are used as is; `false`: relative to the executable's directory (always as is with `-env=dev`) |
| `sleep` | Pause between checks of the input folder |
| `idleTimeout` | How long the daemon waits for new files before stopping; `0` means never stop |
| `lifeTime` | How long to keep results, archives and temporary files |
| `lifeTimeHistory` | How long to keep source files in `history` |
| `padRows` | Pad rows with empty cells up to the end of the range (xlsx) |
| `delimiter` | CSV delimiter, a single one-byte character |
| `withHidden` | Convert hidden xlsx sheets |
| `startServer`, `serverPort` | HTTP server and its port |
| `readTimeout`, `readHeadTimeout`, `writeTimeout` | HTTP server timeouts; `0` means no limit |
| `maxUploadSize` | Maximum upload size in **MB** (must be > 0) |
| `logLevel`, `logFile` | Level (`debug`/`info`/`warn`/`error`) and log file (empty means stdout), JSON format |
| `useMdbLib` | Use `mdbtools` instead of the built-in Access reader |

**Duration format.** A Go duration string (`"10s"`, `"1m30s"`, `"2h"`) or a number of seconds. Negative values are not allowed. JSON keys are case-sensitive.

**Validation on startup.** All paths must be set, durations ≥ 0, `maxUploadSize` > 0, `delimiter` non-empty and one byte long.

## HTTP API

Error responses and the `/upload` and `/check` responses are JSON:
```json
{ "success": true, "message": "...", "data": { "fileName": "...", "fileKey": "...", "status": "..." } }
```
The `data` field is omitted when there is no data; `status` is filled only by `/check`.

### 1. Upload: `POST /upload`

The body is `multipart/form-data` with a `file` field. Allowed extensions: `.xls`, `.xlsx`, `.xlsm`, `.accdb`, `.mdb` (case-insensitive). Any path in the file name is discarded; the file is stored under a generated key, so it cannot escape the input folder.

Success is `202 Accepted`:
```json
{
  "success": true,
  "message": "File uploaded successfully, waiting for processing",
  "data": { "fileName": "example.xlsx", "fileKey": "550e8400-e29b-41d4-a716-446655440000" }
}
```

| Code | When |
|------|------|
| 202 | File accepted |
| 400 | Malformed form, missing `file` field, invalid name or extension |
| 405 | Method is not `POST` |
| 413 | Size exceeds `maxUploadSize` (checked both by `Content-Length` and while streaming) |
| 422 | The message or file could not be saved (the created message is rolled back) |

### 2. Status: `GET /check?key={key}`

```json
{
  "success": true,
  "message": "File converted successfully",
  "data": {
    "fileName": "example.xlsx",
    "fileKey": "550e8400-e29b-41d4-a716-446655440000",
    "status": "done"
  }
}
```

| status | Description |
|--------|-------------|
| `new` | File accepted, waiting for processing |
| `processing` | Conversion in progress |
| `error` | Conversion failed |
| `done` | Finished |

Codes: `200`, `400` (no `key`), `404` (unknown key), `500` (store error).

### 3. Download: `GET /download?key={key}[&sheetNum={N}]`

| Parameter | Description |
|-----------|-------------|
| `key` | File key |
| `sheetNum` | Zero-based sheet number. If omitted, a ZIP with all sheets is returned |

Download file name: `<original name without extension>.zip` or `<name>_<N>.csv`. The ZIP is built on the first request and cached in `archives/` until `lifeTime` expires.

| Code | When |
|------|------|
| 200 | File returned |
| 400 | No `key`, or `sheetNum` is not a number |
| 404 | Key or sheet not found |
| 422 | File not processed yet (`new`/`processing`) or processing failed (`error`) |
| 500 | Store or archive error (for example, the result has already been removed by cleanup) |

Other methods return `405`.

## Output format

```
out/
└── {fileKey}/
    ├── 0.csv      # first sheet / table
    ├── 1.csv
    └── ...
```

- **xlsx sheet numbering**: tab order in the workbook; hidden sheets are skipped (unless `withHidden`), numbering is continuous.
- **Access tables**: alphabetical order, system tables excluded.
- **Delimiter**: from the settings; values containing the delimiter, quotes or line breaks are escaped following CSV rules.
- **Encoding**: UTF-8, no BOM. No header is added: the first line is the first row of the sheet (for Access, the column names).
- **Dates**: `2023-03-15` for a date without time, `2023-03-15 12:30:00` with time, `12:30:00` for a pure time. Access dates are always printed with time.
- **Booleans**: `TRUE`/`FALSE` in XLS, `true`/`false` in Access.

### Example
```csv
Name;Amount;Date
Anna;30.5;2023-03-15
```

## Error handling

1. **Unsupported extension**: the file is moved to `failed`, status `error`.
2. **Corrupted or unreadable file** (invalid zip/OLE2/Access): same.
3. **Panic during conversion**: recovered at the task level, status `error`, the worker keeps running.
4. **Daemon shutdown**: the task goes back to `new`, the file stays in the input folder.
5. **Unavailable store**: the file is moved to `failed`.

Re-uploading a file with the same key (standalone mode) resets the status to `new` and replaces the previous result after a successful conversion.

## Performance

- The number of workers is `runtime.NumCPU()`; to set it manually, change `numWorkers` in `start()` (`converters/job.go`).
- The task queue and the result channel are buffered (100).
- A single task has a 5-minute timeout.
- xlsx is parsed as a stream and does not load a whole sheet into memory; xls is loaded into memory entirely.
- **Empty rows.** Broken files with a huge number of empty rows produce a gigantic CSV. Skipping empty rows is not implemented yet (planned as an optional setting).

## Security

Already implemented:
- upload size limit `maxUploadSize` (checked by `Content-Length` and `http.MaxBytesReader`);
- file extension and name validation, any path in the name is discarded;
- HTTP server read/write timeouts;
- the XLS parser guards against bogus sizes in headers; the XLS and Access parsers are robust against corrupted files (verified by tests with random corruption);
- `sheetNum` accepts digits only, and the key is looked up in the store instead of being used in a path directly.

Recommended:
1. Run the service in a container or under a user with minimal privileges.
2. Put a reverse proxy with authentication in front of the service: the API has no authorization.
3. Limit the number of concurrent uploads at the proxy level.

## Testing

```bash
go vet ./...
go test -race ./...
go test -short ./...   # skips the slowest tests (Northwind, mutation tests)
```

The tests need the same Go 1.27+. Test files live in `converters/xls/testdata/` and `converters/accdb/testdata/` (`sample.xls`, `sample.accdb`, `northwind.accdb`).

Covered: the HTTP server (`httptest`), the Job with a fake store and temporary directories, the daemon manager, the BoltDB store, settings, all three converters, the `xlslib` and `accdbread` parsers (including corrupted files), ZIP packing.

## Extending

### Adding a new converter
1. Create `converters/yourformat/yourformat.go` with a type implementing `Convert(csvPath string) error`:
```go
type Converter struct {
    Ctx      context.Context
    FilePath string
    Settings *settings.Settings
}

func NewConverter(ctx context.Context, filePath string, s *settings.Settings) *Converter {
    return &Converter{Ctx: ctx, FilePath: filePath, Settings: s}
}

func (c *Converter) Convert(csvPath string) error {
    // write csvPath/0.csv, 1.csv, ... ; check c.Ctx.Err() in loops
    return nil
}
```
2. Add the extension to `convertOne` (`converters/job.go`):
```go
case ".yourformat":
    converterSync = yourformat.NewConverter(taskCtx, task.filepath, &j.Settings)
```
3. Add the extension to the allowed list in `server/server.go` (`allowedExt`).
4. Write a test with a real file in `testdata/`.

## Known limitations

1. **XLSM**: macros are not executed; the file is converted like a regular XLSX.
2. **XLSX**: boolean cells (`t="b"`) are not supported, empty rows between non-empty ones are not restored, and absolute sheet paths (`/xl/worksheets/...`) in `workbook.xml.rels` are not recognized.
3. **Date and number formats**: complex custom formats may be interpreted incorrectly.
4. **Delimiter**: a single one-byte character only.
5. **Large files**: XLS is read into memory entirely; very large files take longer and run into the 5-minute timeout.
6. **Consistent values** across converters are not guaranteed (booleans, dates with zero time).
7. **No API authorization.**
