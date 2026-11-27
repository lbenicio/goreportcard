# Adding an analyzer check

The web service and CLI share the analyzer implementation in `src/check`.

## Implement the check

Add a type that implements the `check.Check` interface:

- `Name()` returns the stable check name shown in reports.
- `Description()` describes what the analyzer measures.
- `Weight()` returns its contribution to the overall score.
- `Percentage()` runs the analysis and returns a score from 0 to 1, file summaries, and any execution error.

Use the existing check implementations as examples. Report file-level findings with the repository-relative file name, line number, and a concise error string when available.

## Register and verify

Add the check to the list in `src/check/check.go`. Add unit tests for success, failure, and edge cases in the corresponding `*_test.go` file. Then run `make lint`, `make test`, and `make build`.

Keep check behavior deterministic where possible, and avoid introducing a dependency on the web handler or storage packages. The check package is shared by both server and CLI.
