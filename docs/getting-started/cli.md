# Command-line interface

The standalone `goreportcard` command runs the repository checks against a local Go project. Install analyzer tools with `make install`, then install the CLI:

```sh
go install ./src/cmd/goreportcard
goreportcard --help
```

## Options

| Flag | Purpose |
|---|---|
| `-h`, `--help` | Print usage information. |
| `-v`, `--version` | Print the version embedded in the binary. Source builds without release metadata report `dev`. |
| `-d <directory>` | Select the project directory. Defaults to the current directory. |
| `-t <percentage>` | Exit with status 1 if the overall score is below the threshold. |
| `-j` | Print JSON. In JSON mode the command exits with status 0. |
| `--verbose` | Include per-file details for checks that provide them. |
| `--log-level <level>` | Set the minimum log level to `debug`, `info`, `warn`, or `error`. |
| `--log-color <mode>` | Set log colors to `auto`, `always`, or `never`. |

Examples:

```sh
goreportcard -d ./my-project
goreportcard -d ./my-project -t 80
goreportcard -d ./my-project -j
goreportcard -d ./my-project --verbose --log-level=debug
```

The CLI and web service use the same checks from `src/check`. Release builds embed the release version; see [Release process](../operations/releases.md).
