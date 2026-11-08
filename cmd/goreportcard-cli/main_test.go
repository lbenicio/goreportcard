package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/lbenicio/goreportcard/check"
)

func TestMainPrintsHumanReadableCheckSummary(t *testing.T) {
	preserveCLIState(t)
	result := check.ChecksResult{
		Grade:   check.GradeA,
		Average: 0.825,
		Files:   2,
		Issues:  1,
		Checks: []check.Score{
			{
				Name:       "gofmt",
				Percentage: 0.75,
				FileSummaries: []check.FileSummary{{
					Filename: "main.go",
					Errors:   []check.Error{{LineNumber: 4, ErrorString: "formatting issue"}},
				}},
			},
			{Name: "misspell", Percentage: 1},
		},
	}
	*dir = "fixture"
	*verbose = true
	*th = 0
	*jsn = false
	var gotDir string
	var gotCLI bool
	runCheck = func(path string, cli bool) (check.ChecksResult, error) {
		gotDir, gotCLI = path, cli
		return result, nil
	}
	exitProcess = func(code int) { t.Errorf("unexpected process exit with code %d", code) }

	output := captureCommandStdout(t, main)
	for _, want := range []string{"Grade ............ A 82.5%", "Files", "Issues", "gofmt", "75%", "main.go", "Line 4: formatting issue", "misspell"} {
		if !strings.Contains(output, want) {
			t.Errorf("CLI output %q does not contain %q", output, want)
		}
	}
	if gotDir != "fixture" || !gotCLI {
		t.Fatalf("check.Run called with (%q, %t), want (fixture, true)", gotDir, gotCLI)
	}
}

func TestMainJSONOutputExitsSuccessfully(t *testing.T) {
	preserveCLIState(t)
	result := check.ChecksResult{Grade: check.GradeB, Average: 0.78, Files: 5, Issues: 2}
	*dir = "json-fixture"
	*jsn = true
	runCheck = func(string, bool) (check.ChecksResult, error) { return result, nil }
	var exitCodes []int
	exitProcess = func(code int) { exitCodes = append(exitCodes, code) }

	output := captureCommandStdout(t, main)
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := output, string(encoded)+"\n"; got != want {
		t.Fatalf("JSON output = %q, want %q", got, want)
	}
	if len(exitCodes) != 1 || exitCodes[0] != 0 {
		t.Fatalf("exit codes = %v, want [0]", exitCodes)
	}
}

func TestMainThresholdFailureExitsOne(t *testing.T) {
	preserveCLIState(t)
	*dir = "threshold-fixture"
	*th = 80
	*jsn = false
	runCheck = func(string, bool) (check.ChecksResult, error) {
		return check.ChecksResult{Grade: check.GradeC, Average: 0.79}, nil
	}
	var exitCodes []int
	exitProcess = func(code int) { exitCodes = append(exitCodes, code) }

	output := captureCommandStdout(t, main)
	if !strings.Contains(output, "Grade") {
		t.Fatalf("threshold failure did not print the summary: %q", output)
	}
	if len(exitCodes) != 1 || exitCodes[0] != 1 {
		t.Fatalf("exit codes = %v, want [1]", exitCodes)
	}
}

func TestMainCheckErrorUsesFatalLoggerAndReturns(t *testing.T) {
	preserveCLIState(t)
	*dir = "broken-fixture"
	checkErr := errors.New("unable to inspect source")
	runCheck = func(string, bool) (check.ChecksResult, error) { return check.ChecksResult{}, checkErr }
	var fatalMessage string
	fatalf = func(format string, args ...interface{}) {
		fatalMessage = fmt.Sprintf(format, args...)
	}
	exitProcess = func(code int) { t.Errorf("unexpected process exit with code %d", code) }

	main()
	if want := "Fatal error checking broken-fixture: unable to inspect source"; fatalMessage != want {
		t.Fatalf("fatal message = %q, want %q", fatalMessage, want)
	}
}

func preserveCLIState(t *testing.T) {
	t.Helper()
	oldDir, oldVerbose, oldThreshold, oldJSON := *dir, *verbose, *th, *jsn
	oldRunCheck, oldExitProcess, oldFatalf := runCheck, exitProcess, fatalf
	t.Cleanup(func() {
		*dir, *verbose, *th, *jsn = oldDir, oldVerbose, oldThreshold, oldJSON
		runCheck, exitProcess, fatalf = oldRunCheck, oldExitProcess, oldFatalf
	})
}

func captureCommandStdout(t *testing.T, run func()) string {
	t.Helper()
	originalStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		os.Stdout = originalStdout
		_ = reader.Close()
		_ = writer.Close()
	}()
	os.Stdout = writer

	run()
	if err := writer.Close(); err != nil {
		t.Fatalf("close stdout pipe: %v", err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return string(output)
}

func TestDotPrintfFormatsValuesAndUsesMinimumDots(t *testing.T) {
	originalStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		os.Stdout = originalStdout
		_ = reader.Close()
		_ = writer.Close()
	}()
	os.Stdout = writer

	dotPrintf(24, "Grade", "%s %.1f%%", "A", 99.5)
	dotPrintf(5, "VeryLongLabel", "%s", "value")

	if err := writer.Close(); err != nil {
		t.Fatalf("close stdout pipe: %v", err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}

	want := "Grade ............ A 99.5%\nVeryLongLabel .. value\n"
	if got := string(output); got != want {
		t.Fatalf("dotPrintf output = %q, want %q", got, want)
	}
	if strings.Count(string(output), "\n") != 2 {
		t.Fatalf("dotPrintf output should contain two lines, got %q", output)
	}
}
