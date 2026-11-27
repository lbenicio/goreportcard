package logging

import (
	"bytes"
	"errors"
	"io"
	"log"
	"os"
	"strings"
	"testing"
)

func TestParseLevel(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    level
		wantErr bool
	}{
		{name: "debug", value: "debug", want: debugLevel},
		{name: "info", value: "info", want: infoLevel},
		{name: "empty defaults to info", value: "", want: infoLevel},
		{name: "warn", value: "warn", want: warnLevel},
		{name: "warning alias", value: "WARNING", want: warnLevel},
		{name: "error", value: "error", want: errorLevel},
		{name: "invalid", value: "trace", want: infoLevel, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseLevel(tt.value)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseLevel(%q) error = %v, wantErr %t", tt.value, err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("parseLevel(%q) = %d, want %d", tt.value, got, tt.want)
			}
		})
	}
}

func TestUseColorModes(t *testing.T) {
	if got, err := useColor("always"); err != nil || !got {
		t.Fatalf("useColor(always) = %t, %v; want true, nil", got, err)
	}
	if got, err := useColor("never"); err != nil || got {
		t.Fatalf("useColor(never) = %t, %v; want false, nil", got, err)
	}
	if _, err := useColor("sometimes"); err == nil {
		t.Fatal("useColor(invalid) succeeded, want an error")
	}

	oldNoColor, hadNoColor := os.LookupEnv("NO_COLOR")
	if err := os.Unsetenv("NO_COLOR"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if hadNoColor {
			_ = os.Setenv("NO_COLOR", oldNoColor)
		} else {
			_ = os.Unsetenv("NO_COLOR")
		}
	})

	originalStderr := os.Stderr
	device, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = device
	t.Cleanup(func() {
		os.Stderr = originalStderr
		_ = device.Close()
	})
	if got, err := useColor("auto"); err != nil || !got {
		t.Fatalf("useColor(auto) for character device = %t, %v; want true, nil", got, err)
	}

	if err := device.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := useColor("auto"); err != nil || got {
		t.Fatalf("useColor(auto) for closed stderr = %t, %v; want false, nil", got, err)
	}
	if err := os.Setenv("NO_COLOR", "1"); err != nil {
		t.Fatal(err)
	}
	if got, err := useColor("auto"); err != nil || got {
		t.Fatalf("useColor(auto) with NO_COLOR = %t, %v; want false, nil", got, err)
	}
}

func TestConfigure(t *testing.T) {
	previous := log.Writer()
	t.Cleanup(func() { log.SetOutput(previous) })

	if err := Configure("warn", "never"); err != nil {
		t.Fatalf("Configure(valid settings) error = %v", err)
	}
	configured, ok := log.Writer().(*levelWriter)
	if !ok {
		t.Fatalf("configured logger writer = %T, want *levelWriter", log.Writer())
	}
	if configured.minimum != warnLevel || configured.color {
		t.Fatalf("configured writer = %#v, want warn level without color", configured)
	}

	if err := Configure("trace", "never"); err == nil {
		t.Fatal("Configure(invalid level) succeeded, want an error")
	}
	if err := Configure("info", "sometimes"); err == nil {
		t.Fatal("Configure(invalid color) succeeded, want an error")
	}
}

func TestLevelWriterFiltersAndFormatsMessages(t *testing.T) {
	var output bytes.Buffer
	writer := &levelWriter{output: &output, minimum: warnLevel}
	input := "2026/11/27 10:11:12 DEBUG: detail\n2026/11/27 10:11:13 ordinary info\n2026/11/27 10:11:14 WARNING: caution\nERROR: failed"
	n, err := writer.Write([]byte(input))
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if n != len(input) {
		t.Fatalf("Write() bytes = %d, want %d", n, len(input))
	}
	want := "2026/11/27 10:11:14 [WARN] caution\n[ERROR] failed"
	if got := output.String(); got != want {
		t.Fatalf("formatted output = %q, want %q", got, want)
	}
}

func TestLevelWriterColorsAndHandlesNewlines(t *testing.T) {
	var output bytes.Buffer
	writer := &levelWriter{output: &output, minimum: debugLevel, color: true}
	if _, err := writer.Write([]byte("DEBUG: detail\nvisible\r\nDEBUG:\n")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	want := "\x1b[90m[DEBUG]\x1b[0m detail\n\x1b[36m[INFO]\x1b[0m visible\n\x1b[90m[DEBUG]\x1b[0m\n"
	if got := output.String(); got != want {
		t.Fatalf("colored output = %q, want %q", got, want)
	}
}

func TestLevelWriterEmptyFilteredAndOutputError(t *testing.T) {
	var output bytes.Buffer
	writer := &levelWriter{output: &output, minimum: errorLevel}
	if n, err := writer.Write(nil); err != nil || n != 0 {
		t.Fatalf("Write(empty) = %d, %v; want 0, nil", n, err)
	}
	if n, err := writer.Write([]byte("INFO: filtered\n")); err != nil || n == 0 {
		t.Fatalf("Write(filtered) = %d, %v; want input length, nil", n, err)
	}
	if output.Len() != 0 {
		t.Fatalf("filtered output = %q, want empty", output.String())
	}

	wantErr := errors.New("write failed")
	writer.output = failingWriter{err: wantErr}
	if n, err := writer.Write([]byte("ERROR: visible")); n != 0 || !errors.Is(err, wantErr) {
		t.Fatalf("Write(output error) = %d, %v; want 0, %v", n, err, wantErr)
	}
}

func TestSplitTimestamp(t *testing.T) {
	prefix, message := splitTimestamp("2026/11/27 10:11:12 hello")
	if prefix != "2026/11/27 10:11:12 " || message != "hello" {
		t.Fatalf("splitTimestamp(valid) = %q, %q", prefix, message)
	}
	if prefix, message := splitTimestamp("not a timestamp"); prefix != "" || message != "not a timestamp" {
		t.Fatalf("splitTimestamp(invalid) = %q, %q", prefix, message)
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		input   string
		want    level
		message string
	}{
		{input: "DEBUG: detail", want: debugLevel, message: "detail"},
		{input: "WARNING: caution", want: warnLevel, message: "caution"},
		{input: "WARN: caution", want: warnLevel, message: "caution"},
		{input: "ERROR: failed", want: errorLevel, message: "failed"},
		{input: "ERROR handler: failed", want: errorLevel, message: "handler: failed"},
		{input: "FATAL: failed", want: errorLevel, message: "failed"},
		{input: "Fatal error occurred", want: errorLevel, message: "error occurred"},
		{input: "ordinary message", want: infoLevel, message: "ordinary message"},
	}
	for _, tt := range tests {
		gotLevel, gotMessage := classify(tt.input)
		if gotLevel != tt.want || gotMessage != tt.message {
			t.Errorf("classify(%q) = (%d, %q), want (%d, %q)", tt.input, gotLevel, gotMessage, tt.want, tt.message)
		}
	}
}

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

var _ io.Writer = failingWriter{}

func TestLogConfiguredWriterFiltersStandardLogs(t *testing.T) {
	previous := log.Writer()
	previousFlags := log.Flags()
	t.Cleanup(func() {
		log.SetOutput(previous)
		log.SetFlags(previousFlags)
	})

	reader, pipeWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	originalStderr := os.Stderr
	t.Cleanup(func() {
		os.Stderr = originalStderr
		_ = pipeWriter.Close()
		_ = reader.Close()
	})
	os.Stderr = pipeWriter
	if err := Configure("error", "never"); err != nil {
		t.Fatal(err)
	}
	log.Print("INFO: hidden")
	log.Print("ERROR: visible")
	if err := pipeWriter.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stderr = originalStderr
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "[ERROR] visible") || strings.Contains(string(got), "hidden") {
		t.Fatalf("standard logger output = %q, want only formatted error", got)
	}
}
