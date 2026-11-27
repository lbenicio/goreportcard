package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/lbenicio/goreportcard/src/check"
	"github.com/lbenicio/goreportcard/src/logging"
)

var (
	dir         = flag.String("d", ".", "Root directory of your Go application")
	verbose     = flag.Bool("verbose", false, "Verbose output")
	th          = flag.Float64("t", 0, "Threshold of failure command")
	jsn         = flag.Bool("j", false, "JSON output. The binary will always exit with code 0")
	showVersion = flag.Bool("v", false, "Print version and exit")
	logLevel    = flag.String("log-level", envOrDefault("LOG_LEVEL", "info"), "Minimum log level: debug, info, warn, or error")
	logColor    = flag.String("log-color", envOrDefault("LOG_COLOR", "auto"), "Log coloring: auto, always, or never")
	version     = "dev"
	runCheck    = check.Run
	exitProcess = os.Exit
	fatalf      = log.Fatalf
)

func init() {
	flag.BoolVar(showVersion, "version", false, "Print version and exit")
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

// dotPrintf fills in the blank space between two strings with dots. The total
// length displayed is indicated by fullLen. The left string is specified by
// lfStr. The right string is formatted. At least two dots are shown, even if
// this means the total length exceeds fullLen.
func dotPrintf(fullLen int, lfStr, rtFmtStr string, args ...interface{}) {
	rtStr := fmt.Sprintf(rtFmtStr, args...)
	dotLen := fullLen - len(lfStr) - len(rtStr)
	if dotLen < 2 {
		dotLen = 2
	}
	fmt.Printf("%s %s %s\n", lfStr, strings.Repeat(".", dotLen), rtStr)
}

func main() {
	flag.Parse()
	if *showVersion {
		fmt.Printf("goreportcard version %s\n", version)
		return
	}
	if err := logging.Configure(*logLevel, *logColor); err != nil {
		fatalf("ERROR: %v", err)
		return
	}

	result, err := runCheck(*dir, true)
	if err != nil {
		fatalf("Fatal error checking %s: %s", *dir, err.Error())
		return
	}

	if *jsn {
		marshalledResults, _ := json.Marshal(result)
		fmt.Println(string(marshalledResults))
		exitProcess(0)
		return
	}

	dotPrintf(24, "Grade", "%s %.1f%%", result.Grade, result.Average*100)
	dotPrintf(24, "Files", "%d", result.Files)
	dotPrintf(24, "Issues", "%d", result.Issues)

	for _, c := range result.Checks {
		dotPrintf(24, c.Name, "%d%%", int64(c.Percentage*100))
		if *verbose && len(c.FileSummaries) > 0 {
			for _, f := range c.FileSummaries {
				fmt.Printf("\t%s\n", f.Filename)
				for _, e := range f.Errors {
					fmt.Printf("\t\tLine %d: %s\n", e.LineNumber, e.ErrorString)
				}
			}
		}
	}

	if result.Average*100 < *th {
		exitProcess(1)
		return
	}
}
