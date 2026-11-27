// Package logging configures level filtering and colorized output for the
// standard library logger used throughout the application.
package logging

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"time"
)

type level int

const (
	debugLevel level = iota
	infoLevel
	warnLevel
	errorLevel
)

type levelWriter struct {
	output  io.Writer
	minimum level
	color   bool
	mu      sync.Mutex
}

// Configure sets the minimum log level and color mode. Supported levels are
// debug, info, warn, and error. Supported color modes are auto, always, and
// never. In auto mode, NO_COLOR disables color and color is enabled only when
// stderr is connected to a terminal.
func Configure(levelName, colorMode string) error {
	minimum, err := parseLevel(levelName)
	if err != nil {
		return err
	}
	color, err := useColor(colorMode)
	if err != nil {
		return err
	}

	log.SetOutput(&levelWriter{output: os.Stderr, minimum: minimum, color: color})
	return nil
}

func parseLevel(value string) (level, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug":
		return debugLevel, nil
	case "info", "":
		return infoLevel, nil
	case "warn", "warning":
		return warnLevel, nil
	case "error":
		return errorLevel, nil
	default:
		return infoLevel, fmt.Errorf("invalid LOG_LEVEL %q: use debug, info, warn, or error", value)
	}
}

func useColor(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "always":
		return true, nil
	case "never":
		return false, nil
	case "auto", "":
		if _, disabled := os.LookupEnv("NO_COLOR"); disabled {
			return false, nil
		}
		info, err := os.Stderr.Stat()
		return err == nil && info.Mode()&os.ModeCharDevice != 0, nil
	default:
		return false, fmt.Errorf("invalid LOG_COLOR %q: use auto, always, or never", value)
	}
}

func (w *levelWriter) Write(input []byte) (int, error) {
	var output bytes.Buffer
	for _, rawLine := range bytes.SplitAfter(input, []byte{'\n'}) {
		if len(rawLine) == 0 {
			continue
		}
		line := strings.TrimSuffix(string(rawLine), "\n")
		line = strings.TrimSuffix(line, "\r")
		prefix, message := splitTimestamp(line)
		messageLevel, message := classify(message)
		if messageLevel < w.minimum {
			continue
		}

		label := "INFO"
		colorCode := "36"
		switch messageLevel {
		case debugLevel:
			label, colorCode = "DEBUG", "90"
		case warnLevel:
			label, colorCode = "WARN", "33"
		case errorLevel:
			label, colorCode = "ERROR", "31"
		}
		if w.color {
			label = fmt.Sprintf("\x1b[%sm[%s]\x1b[0m", colorCode, label)
		} else {
			label = "[" + label + "]"
		}

		output.WriteString(prefix)
		output.WriteString(label)
		if message != "" {
			output.WriteByte(' ')
			output.WriteString(message)
		}
		if bytes.HasSuffix(rawLine, []byte{'\n'}) {
			output.WriteByte('\n')
		}
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if output.Len() == 0 {
		return len(input), nil
	}
	_, err := w.output.Write(output.Bytes())
	if err != nil {
		return 0, err
	}
	return len(input), nil
}

func splitTimestamp(line string) (string, string) {
	const timestampLength = len("2006/01/02 15:04:05")
	if len(line) > timestampLength && line[timestampLength] == ' ' {
		if _, err := time.Parse("2006/01/02 15:04:05", line[:timestampLength]); err == nil {
			return line[:timestampLength+1], line[timestampLength+1:]
		}
	}
	return "", line
}

func classify(message string) (level, string) {
	trimmed := strings.TrimSpace(message)
	upper := strings.ToUpper(trimmed)
	for _, marker := range []struct {
		prefix string
		level  level
	}{
		{prefix: "DEBUG:", level: debugLevel},
		{prefix: "WARNING:", level: warnLevel},
		{prefix: "WARN:", level: warnLevel},
		{prefix: "ERROR:", level: errorLevel},
		{prefix: "ERROR ", level: errorLevel},
		{prefix: "FATAL:", level: errorLevel},
		{prefix: "FATAL ", level: errorLevel},
	} {
		if strings.HasPrefix(upper, marker.prefix) {
			return marker.level, strings.TrimSpace(trimmed[len(marker.prefix):])
		}
	}
	return infoLevel, trimmed
}
