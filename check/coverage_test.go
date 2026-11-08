package check

import (
	"bufio"
	"errors"
	"fmt"

	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestGradeFromPercentage(t *testing.T) {
	cases := []struct {
		percentage float64
		want       Grade
	}{
		{91, GradeAPlus},
		{90, GradeA},
		{81, GradeA},
		{80, GradeB},
		{71, GradeB},
		{70, GradeC},
		{61, GradeC},
		{60, GradeD},
		{51, GradeD},
		{50, GradeE},
		{41, GradeE},
		{40, GradeF},
		{0, GradeF},
	}
	for _, tc := range cases {
		if got := GradeFromPercentage(tc.percentage); got != tc.want {
			t.Errorf("GradeFromPercentage(%v) = %q, want %q", tc.percentage, got, tc.want)
		}
	}
}

func TestAdditionalCheckImplementations(t *testing.T) {
	checks := []struct {
		check       Check
		name        string
		weight      float64
		description string
	}{
		{ErrCheck{}, "errcheck", .15, "errcheck"},
		{GoLint{}, "golint", .10, "Golint"},
		{Staticcheck{}, "staticcheck", .15, "Staticcheck"},
	}
	for _, tc := range checks {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.check.Name(); got != tc.name {
				t.Errorf("Name() = %q, want %q", got, tc.name)
			}
			if got := tc.check.Weight(); got != tc.weight {
				t.Errorf("Weight() = %v, want %v", got, tc.weight)
			}
			if got := tc.check.Description(); !strings.Contains(got, tc.description) {
				t.Errorf("Description() = %q, want it to contain %q", got, tc.description)
			}
			percentage, summaries, err := tc.check.Percentage()
			if err != nil {
				t.Fatal(err)
			}
			if percentage != 1 || len(summaries) != 0 {
				t.Errorf("Percentage() = (%v, %v), want (1, no summaries)", percentage, summaries)
			}
		})
	}
}

func TestLicensePercentageOutcomes(t *testing.T) {
	t.Run("license file", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "LiCeNsE.txt"), []byte("license"), 0o600); err != nil {
			t.Fatal(err)
		}
		percentage, summaries, err := (License{Dir: dir}).Percentage()
		if err != nil {
			t.Fatal(err)
		}
		if percentage != 1 || len(summaries) != 0 {
			t.Errorf("Percentage() = (%v, %v), want (1, no summaries)", percentage, summaries)
		}
	})

	t.Run("Go source named like a license is not a license", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "LICENSE.go"), []byte("package license\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("readme"), 0o600); err != nil {
			t.Fatal(err)
		}
		percentage, summaries, err := (License{Dir: dir}).Percentage()
		if err != nil {
			t.Fatal(err)
		}
		want := []FileSummary{{Filename: "", FileURL: "http://choosealicense.com/", Errors: []Error{}}}
		if percentage != 0 || !reflect.DeepEqual(summaries, want) {
			t.Errorf("Percentage() = (%v, %#v), want (0, %#v)", percentage, summaries, want)
		}
	})

	if _, _, err := (License{Dir: filepath.Join(t.TempDir(), "absent")}).Percentage(); err == nil {
		t.Error("Percentage() error = nil for a missing directory, want an error")
	}
}

func TestRunInputErrorsAndCLIFileRestoration(t *testing.T) {
	if _, err := Run(filepath.Join(t.TempDir(), "missing"), false); err == nil || !strings.Contains(err.Error(), "could not get filenames") {
		t.Errorf("Run(missing directory) error = %v, want wrapped filename discovery error", err)
	}
	if _, err := Run(t.TempDir(), false); err == nil || !strings.Contains(err.Error(), "no .go files found") {
		t.Errorf("Run(empty directory) error = %v, want no-Go-files error", err)
	}

	dir := t.TempDir()
	mainFile := filepath.Join(dir, "main.go")
	generatedFile := filepath.Join(dir, "main.generated.go")
	if err := os.WriteFile(mainFile, []byte("package main\n\nfunc main() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(generatedFile, []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(dir, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(generatedFile); err != nil {
		t.Errorf("CLI Run did not restore skipped source %q: %v", generatedFile, err)
	}
	if _, err := os.Stat(generatedFile + ".grc.bk"); !os.IsNotExist(err) {
		t.Errorf("CLI Run left backup file behind (stat error = %v)", err)
	}
}

func TestRenameAndRevertFiles(t *testing.T) {
	dir := t.TempDir()
	filename := filepath.Join(dir, "generated.go")
	content := []byte("package generated\n")
	if err := os.WriteFile(filename, content, 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "missing.go")
	if err := RenameFiles([]string{filename, missing}); err == nil {
		t.Error("RenameFiles() error = nil, want an error for a missing file")
	}
	if _, err := os.Stat(filename); !os.IsNotExist(err) {
		t.Errorf("RenameFiles() left original file in place (stat error = %v)", err)
	}
	if got, err := os.ReadFile(filename + ".grc.bk"); err != nil || !reflect.DeepEqual(got, content) {
		t.Errorf("renamed file = (%q, %v), want original content and no error", got, err)
	}
	if err := RevertFiles([]string{filename, missing}); err == nil {
		t.Error("RevertFiles() error = nil, want an error for a missing backup")
	}
	if got, err := os.ReadFile(filename); err != nil || !reflect.DeepEqual(got, content) {
		t.Errorf("restored file = (%q, %v), want original content and no error", got, err)
	}
}

func TestGoFilesSkipsGeneratedAndExcludedPaths(t *testing.T) {
	root := t.TempDir()
	write := func(name, contents string) {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("included.go", "package included\n")
	write("generated.go", "// Code generated by fixture. DO NOT EDIT.\npackage generated\n")
	write("vendor/vendor.go", "package vendor\n")
	write("testdata/fixture.go", "package fixture\n")
	write("generated.generated.go", "package generated\n")
	write("notes.txt", "not Go\n")

	files, skipped, err := GoFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(files, []string{filepath.Join(root, "included.go")}) {
		t.Errorf("GoFiles() = %v, want only included.go", files)
	}
	wantSkipped := []string{filepath.Join(root, "generated.generated.go"), filepath.Join(root, "generated.go")}
	if !reflect.DeepEqual(skipped, wantSkipped) {
		t.Errorf("GoFiles() skipped = %v, want %v", skipped, wantSkipped)
	}
	if files, skipped, err := GoFiles(filepath.Join(root, "missing")); err == nil || len(files) != 0 || len(skipped) != 0 {
		t.Errorf("GoFiles(missing root) = (%v, %v, %v), want a walk error and no files", files, skipped, err)
	}
}

func TestGoFilesContinuesAfterUnreadableChild(t *testing.T) {
	root := t.TempDir()
	unreadableDir := filepath.Join(root, "unreadable")
	if err := os.Mkdir(unreadableDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unreadableDir, "hidden.go"), []byte("package hidden\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	visible := filepath.Join(root, "visible.go")
	if err := os.WriteFile(visible, []byte("package visible\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unreadableDir, 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(unreadableDir, 0o700)
	if _, err := os.ReadDir(unreadableDir); err == nil {
		t.Skip("current user can read mode-000 directory")
	}

	files, skipped, err := GoFiles(root)
	if err != nil {
		t.Fatalf("GoFiles() error = %v, want to continue past unreadable child", err)
	}
	if !reflect.DeepEqual(files, []string{visible}) || len(skipped) != 0 {
		t.Errorf("GoFiles() = (%v, %v), want only the readable sibling", files, skipped)
	}
}

func TestAutoGeneratedRecognizesHeaderForms(t *testing.T) {
	cases := []struct {
		header string
		want   bool
	}{
		{"// Code generated by tool; DO NOT EDIT.", true},
		{"//generated file", true},
		{"/* @generated by tool */", true},
		{"// auto-generated file", true},
		{"package ordinary", false},
		{"", false},
	}
	for i, tc := range cases {
		filename := filepath.Join(t.TempDir(), fmt.Sprintf("case-%d.go", i))
		if err := os.WriteFile(filename, []byte(tc.header+"\npackage sample\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := autoGenerated(filename); got != tc.want {
			t.Errorf("autoGenerated(%q) = %t, want %t", tc.header, got, tc.want)
		}
	}
	if autoGenerated(filepath.Join(t.TempDir(), "absent.go")) {
		t.Error("autoGenerated(missing file) = true, want false")
	}
}

func TestLineCountReportsCommandAndOutputErrors(t *testing.T) {
	if _, err := lineCount(filepath.Join(t.TempDir(), "missing.go")); err == nil {
		t.Error("lineCount(missing file) error = nil, want wc failure")
	}

	binDir := t.TempDir()
	writeExecutable(t, binDir, "wc", "#!/bin/sh\nprintf 'not-a-count input\\n'\n")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if _, err := lineCount("input.go"); err == nil || !strings.Contains(err.Error(), "could not count lines") {
		t.Errorf("lineCount(invalid wc output) error = %v, want count parse error", err)
	}
}

func TestDiagnosticParsingAndSummaryHelpers(t *testing.T) {
	for _, input := range []string{"not a diagnostic", "file.go:999999999999999999999999: overflow"} {
		if _, _, _, ok := parseDiagnostic(input); ok {
			t.Errorf("parseDiagnostic(%q) succeeded, want failure", input)
		}
	}
	var summary FileSummary
	if err := summary.AddError("main.go:3: problem"); err != nil {
		t.Fatal(err)
	}
	if err := summary.AddError("not a diagnostic"); err == nil {
		t.Error("AddError(malformed output) error = nil, want parse error")
	}
	if !reflect.DeepEqual(summary.Errors, []Error{{LineNumber: 3, ErrorString: " problem"}}) {
		t.Errorf("AddError() errors = %#v, want one parsed diagnostic", summary.Errors)
	}
}

func TestGoPackageURLsAndFilenames(t *testing.T) {
	urls := []struct {
		name string
		want string
	}{
		{"example.com/pkg", ""},
		{"gopkg.in", ""},
		{"gopkg.in/yaml.v2", "https://github.com/go-yaml/yaml/blob/v2"},
		{"gopkg.in/yaml.v0/sub/pkg", "https://github.com/go-yaml/yaml/blob/master/sub/pkg"},
		{"gopkg.in/user/repo.v3/sub/pkg", "https://github.com/user/repo/blob/v3/sub/pkg"},
		{"gopkg.in/user/repo", ""},
	}
	for _, tc := range urls {
		if got := goPkgInToGitHub(tc.name); got != tc.want {
			t.Errorf("goPkgInToGitHub(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}

	fileURLs := []struct {
		filename string
		want     string
	}{
		{"/github.com/foo/repo/pkg/file.go", "https://github.com/foo/repo/blob/master/pkg/file.go"},
		{"/golang.org/x/tools@v0.1.0/go/analysis.go", "https://github.com/golang/tools/blob/master@v0.1.0/go/analysis.go"},
		{"/gopkg.in/yaml.v2/yaml.go", "https://github.com/go-yaml/yaml/blob/v2/yaml.go"},
		{"/example.com/repo/file.go", ""},
	}
	for _, tc := range fileURLs {
		if got := fileURL(tc.filename); got != tc.want {
			t.Errorf("fileURL(%q) = %q, want %q", tc.filename, got, tc.want)
		}
	}

	filenames := []struct {
		filename string
		want     string
	}{
		{"/golang.org/x/tools/pkg/file.go", "tools/pkg/file.go"},
		{"/gopkg.in/yaml.v2/pkg/file.go", "pkg/file.go"},
		{"/golang.org/x", "/golang.org/x"},
		{"local/file.go", "local/file.go"},
	}
	for _, tc := range filenames {
		if got := makeFilename(tc.filename); got != tc.want {
			t.Errorf("makeFilename(%q) = %q, want %q", tc.filename, got, tc.want)
		}
	}
}

func TestToolOutputNormalizationAndSummaries(t *testing.T) {
	for _, tc := range []struct {
		line string
		tool string
		skip bool
	}{
		{"  \t", "vet", true},
		{"# example/package", "vet", true},
		{"go: downloading example.com/pkg v1.0.0", "vet", true},
	} {
		if _, skip, err := normalizeToolOutputLine(tc.line, tc.tool); err != nil || skip != tc.skip {
			t.Errorf("normalizeToolOutputLine(%q) = (_, %t, %v), want skip=%t", tc.line, skip, err, tc.skip)
		}
	}
	if got, skip, err := normalizeToolOutputLine("  main.go  ", "gofmt"); err != nil || skip || got != "main.go:1:0: file is not gofmted with -s (gofmt)" {
		t.Errorf("gofmt normalization = (%q, %t, %v), want normalized diagnostic", got, skip, err)
	}
	if _, _, err := normalizeToolOutputLine("bad gocyclo line", "gocyclo"); err == nil {
		t.Error("normalizeToolOutputLine(gocyclo malformed output) error = nil, want parse error")
	}
	if got, skip, err := normalizeToolOutputLine("21 check GoTool check/utils.go:337:1", "gocyclo"); err != nil || skip || got != "check/utils.go:337:1: cyclomatic complexity 21 in function GoTool (gocyclo)" {
		t.Errorf("gocyclo normalization = (%q, %t, %v), want a normalized diagnostic", got, skip, err)
	}
	if got, err := normalizeGocycloOutput("bad complexity foo file.go:1"); err == nil || got != "" {
		t.Errorf("normalizeGocycloOutput(invalid complexity) = (%q, %v), want parse error", got, err)
	}
	if _, err := normalizeGocycloOutput("too few fields"); err == nil {
		t.Error("normalizeGocycloOutput(short output) error = nil, want parse error")
	}

	dir := t.TempDir()
	mainFile := filepath.Join(dir, "main.go")
	generated := filepath.Join(dir, "generated.go")
	if err := os.WriteFile(mainFile, []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(generated, []byte("// Code generated by fixture.\npackage generated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !skipDiagnosticFile("main.pb.go") || !skipDiagnosticFile(generated) || skipDiagnosticFile(mainFile) {
		t.Error("skipDiagnosticFile() did not distinguish generated, suffixed, and ordinary sources")
	}

	for _, tc := range []struct {
		line string
		want string
	}{
		{"main.go:1: first", mainFile},
		{mainFile + ":2: absolute", mainFile},
	} {
		got, err := diagnosticFilename(tc.line, dir, "vet")
		if err != nil || got != tc.want {
			t.Errorf("diagnosticFilename(%q) = (%q, %v), want %q", tc.line, got, err, tc.want)
		}
	}
	if _, err := diagnosticFilename("not a diagnostic", dir, "vet"); err == nil {
		t.Error("diagnosticFilename(malformed output) error = nil, want parse error")
	}

	fsMap := make(map[string]FileSummary)
	for _, line := range []string{mainFile + ":1: first", mainFile + ":2: second"} {
		if err := addToolFileSummary(fsMap, line[:strings.Index(line, ":")], line); err != nil {
			t.Fatal(err)
		}
	}
	if err := addToolFileSummary(fsMap, mainFile, "bad output"); err == nil {
		t.Error("addToolFileSummary(malformed output) error = nil, want parse error")
	}
	if got := fsMap[mainFile]; len(got.Errors) != 2 || got.FileURL != "" {
		t.Errorf("aggregated summary = %#v, want two diagnostics and no external URL", got)
	}

	out := bufio.NewScanner(strings.NewReader("\n# package\ngo: downloading module v1\n" + mainFile + ":1: issue\n" + generated + ":1: generated issue\nmain.pb.go:2: generated suffix\n"))
	summaries, err := getFileSummaryMap(out, dir, "vet")
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 {
		t.Errorf("getFileSummaryMap() returned %d summaries, want only the ordinary-file diagnostic", len(summaries))
	}
	if _, err := getFileSummaryMap(bufio.NewScanner(strings.NewReader("malformed")), dir, "vet"); err == nil {
		t.Error("getFileSummaryMap(malformed output) error = nil, want parse error")
	}
	if _, err := getFileSummaryMap(bufio.NewScanner(strings.NewReader("bad gocyclo output")), dir, "gocyclo"); err == nil {
		t.Error("getFileSummaryMap(gocyclo normalization failure) error = nil, want parse error")
	}
	if _, err := getFileSummaryMap(bufio.NewScanner(errorReader{}), dir, "vet"); err == nil {
		t.Error("getFileSummaryMap(read failure) error = nil, want scanner error")
	}

	for _, name := range []string{"a.go", "b.go"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("package sample\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ordered, err := toolFileSummaries([]byte("b.go:1: issue\na.go:1: issue\n"), dir, "vet")
	if err != nil {
		t.Fatal(err)
	}
	if len(ordered) != 2 || ordered[0].Filename != filepath.Join(dir, "a.go") || ordered[1].Filename != filepath.Join(dir, "b.go") {
		t.Errorf("toolFileSummaries() = %#v, want summaries sorted by filename", ordered)
	}
	if _, err := toolFileSummaries([]byte("malformed"), dir, "vet"); err == nil {
		t.Error("toolFileSummaries(malformed output) error = nil, want parse error")
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("fixture read failure") }

func TestPackagePathValidationAndBuildContext(t *testing.T) {
	root := t.TempDir()
	filename := filepath.Join(root, "main.go")
	if err := os.WriteFile(filename, []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := packageDirectories(root, []string{filepath.Join(t.TempDir(), "outside.go")}); err == nil {
		t.Error("packageDirectories(outside file) error = nil, want containment error")
	}

	absRoot, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := resolvePackageFilename(absRoot, root, "main.go")
	if err != nil || resolved != filename {
		t.Errorf("resolvePackageFilename(relative) = (%q, %v), want %q", resolved, err, filename)
	}
	if _, err := resolvePackageFilename(absRoot, root, filepath.Join(t.TempDir(), "outside.go")); err == nil {
		t.Error("resolvePackageFilename(outside file) error = nil, want containment error")
	}

	t.Setenv("GOOS", "fixtureos")
	t.Setenv("GOARCH", "fixturearch")
	t.Setenv("CGO_ENABLED", "0")
	ctx := packageBuildContext()
	if ctx.GOOS != "fixtureos" || ctx.GOARCH != "fixturearch" || ctx.CgoEnabled {
		t.Errorf("packageBuildContext() = (%q, %q, %t), want overrides", ctx.GOOS, ctx.GOARCH, ctx.CgoEnabled)
	}
	t.Setenv("CGO_ENABLED", "1")
	if !packageBuildContext().CgoEnabled {
		t.Error("packageBuildContext() disabled cgo when CGO_ENABLED=1")
	}

	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/root\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	groupDir, err := packageGroupDirectory(absRoot, root, filename, packageBuildContext())
	if err != nil || groupDir != root {
		t.Errorf("packageGroupDirectory(root module) = (%q, %v), want %q", groupDir, err, root)
	}
	ignored := filepath.Join(root, "ignored.go")
	if err := os.WriteFile(ignored, []byte("//go:build ignore\n\npackage ignored\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	groupDir, err = packageGroupDirectory(absRoot, root, ignored, packageBuildContext())
	if err != nil || groupDir != "" {
		t.Errorf("packageGroupDirectory(inactive file) = (%q, %v), want no package group", groupDir, err)
	}
	if _, err := packageGroupDirectory(absRoot, root, filepath.Join(root, "missing.go"), packageBuildContext()); err == nil {
		t.Error("packageGroupDirectory(missing file) error = nil, want MatchFile error")
	}

	module := filepath.Join(root, "nested")
	if err := os.MkdirAll(module, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(module, "go.mod"), []byte("module example.com/nested\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	nestedFile := filepath.Join(module, "nested.go")
	if err := os.WriteFile(nestedFile, []byte("package nested\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := packageModuleRoot(absRoot, nestedFile); err != nil || got != module {
		t.Errorf("packageModuleRoot(nested module) = (%q, %v), want %q", got, err, module)
	}
	if got, err := packageModuleRoot(absRoot, filename); err != nil || got != absRoot {
		t.Errorf("packageModuleRoot(root module) = (%q, %v), want %q", got, err, absRoot)
	}
	child := filepath.Join(root, "child")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := packageModuleRoot(absRoot, filepath.Join(child, "child.go")); err != nil || got != absRoot {
		t.Errorf("packageModuleRoot(non-module subdirectory) = (%q, %v), want %q", got, err, absRoot)
	}
	fallbackRoot := t.TempDir()
	outsideFile := filepath.Join(t.TempDir(), "outside.go")
	if got, err := packageModuleRoot(fallbackRoot, outsideFile); err != nil || got != fallbackRoot {
		t.Errorf("packageModuleRoot(outside root) = (%q, %v), want fallback root %q", got, err, fallbackRoot)
	}
	groupDir, err = packageGroupDirectory(absRoot, root, nestedFile, packageBuildContext())
	if err != nil || groupDir != filepath.Join(root, "nested") {
		t.Errorf("packageGroupDirectory(nested module) = (%q, %v), want nested module directory", groupDir, err)
	}
}

func TestPackageGroupReportsModuleMarkerErrors(t *testing.T) {
	dir := t.TempDir()
	filename := filepath.Join(dir, "main.go")
	if err := os.WriteFile(filename, []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "go.mod")
	if err := os.Symlink("go.mod", marker); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	_, err := packageGroupDirectory(dir, dir, filename, packageBuildContext())
	if err == nil || !strings.Contains(err.Error(), "could not inspect module root") {
		t.Errorf("packageGroupDirectory(broken go.mod symlink) error = %v, want module-root inspection error", err)
	}
}

func TestSetModuleMode(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GO111MODULE", "on")
	cmd := &exec.Cmd{}
	setModuleMode(cmd, dir)
	if !containsEnv(cmd.Env, "GO111MODULE=off") {
		t.Errorf("setModuleMode() env = %v, want module mode disabled", cmd.Env)
	}

	t.Setenv("GO111MODULE", "on")
	cmd = &exec.Cmd{}
	setModuleMode(cmd, t.TempDir())
	if !containsEnv(cmd.Env, "GO111MODULE=off") {
		t.Errorf("setModuleMode() did not replace existing GO111MODULE: %v", cmd.Env)
	}

	for _, marker := range []string{"go.mod", "go.work"} {
		moduleDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(moduleDir, marker), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		cmd = &exec.Cmd{}
		setModuleMode(cmd, moduleDir)
		if cmd.Env != nil {
			t.Errorf("setModuleMode() set environment for directory containing %s: %v", marker, cmd.Env)
		}
	}
}

func TestGoToolEarlyReturnsAndErrors(t *testing.T) {
	if percentage, summaries, err := GoTool(t.TempDir(), nil, nil); err == nil || percentage != 0 || len(summaries) != 0 {
		t.Errorf("GoTool(no command) = (%v, %v, %v), want an error and no summaries", percentage, summaries, err)
	}
	if percentage, summaries, err := GoTool(t.TempDir(), nil, []string{"missing-tool"}); err != nil || percentage != 1 || len(summaries) != 0 {
		t.Errorf("GoTool(no files) = (%v, %v, %v), want (1, no summaries, nil)", percentage, summaries, err)
	}
	outside := filepath.Join(t.TempDir(), "outside.go")
	if err := os.WriteFile(outside, []byte("package outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := GoTool(t.TempDir(), []string{outside}, []string{"golint"}); err == nil {
		t.Error("GoTool(outside package file) error = nil, want containment error")
	}
	inactiveDir := t.TempDir()
	inactive := filepath.Join(inactiveDir, "ignored.go")
	if err := os.WriteFile(inactive, []byte("//go:build ignore\n\npackage ignored\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if percentage, summaries, err := GoTool(inactiveDir, []string{inactive}, []string{"golint"}); err != nil || percentage != 1 || len(summaries) != 0 {
		t.Errorf("GoTool(inactive only) = (%v, %v, %v), want skipped success", percentage, summaries, err)
	}
	if _, _, err := GoTool(t.TempDir(), []string{"file.go"}, []string{"unknown-tool"}); err == nil || !strings.Contains(err.Error(), "unsupported Go tool") {
		t.Errorf("GoTool(unsupported tool) error = %v, want unsupported-tool error", err)
	}
	if percentage, summaries, err := GoTool(t.TempDir(), make([]string, 301), []string{"misspell"}); err != nil || percentage != 1 || len(summaries) != 0 {
		t.Errorf("GoTool(large misspell repository) = (%v, %v, %v), want skipped success", percentage, summaries, err)
	}

	toolDir := t.TempDir()
	filename := filepath.Join(toolDir, "main.go")
	if err := os.WriteFile(filename, []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	writeExecutable(t, binDir, "gocyclo", "#!/bin/sh\nprintf 'malformed analyzer output\\n'\n")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if _, _, err := GoTool(toolDir, []string{filename}, []string{"gocyclo"}); err == nil {
		t.Error("GoTool(malformed analyzer output) error = nil, want diagnostic parsing error")
	}
	t.Setenv("PATH", t.TempDir())
	if _, _, err := GoTool(t.TempDir(), []string{"file.go"}, []string{"gofmt"}); err == nil {
		t.Error("GoTool(missing executable) error = nil, want execution error")
	}
}

func TestToolArgumentsAndThresholds(t *testing.T) {
	if !skipLargeRepositoryTool("misspell", 301) || skipLargeRepositoryTool("misspell", 300) {
		t.Error("skipLargeRepositoryTool() did not apply misspell threshold at 300 files")
	}
	if !skipLargeRepositoryTool("ineffassign", 101) || skipLargeRepositoryTool("ineffassign", 100) {
		t.Error("skipLargeRepositoryTool() did not apply ineffassign threshold at 100 files")
	}
	if skipLargeRepositoryTool("vet", 1000) {
		t.Error("skipLargeRepositoryTool() skipped an unbounded tool")
	}

	if got, err := toolArguments("repo", "gofmt", []string{"gofmt", "-s"}, []string{"a.go"}, nil, false); err != nil || !reflect.DeepEqual(got, []string{"-s", "a.go"}) {
		t.Errorf("toolArguments(gofmt) = (%v, %v), want [-s a.go]", got, err)
	}
	if got, err := toolArguments("repo", "vet", []string{"go", "vet"}, []string{"repo/a.go"}, []string{"repo/a.go"}, true); err != nil || !reflect.DeepEqual(got, []string{"vet", "."}) {
		t.Errorf("toolArguments(go vet) = (%v, %v), want [vet .]", got, err)
	}
	if got, err := packageToolArguments("repo", []string{"-x", "./..."}, []string{"repo/a.go"}, false); err != nil || !reflect.DeepEqual(got, []string{"-x", "./..."}) {
		t.Errorf("packageToolArguments(explicit package) = (%v, %v), want original arguments", got, err)
	}
	if _, err := packageToolArguments("repo", nil, []string{filepath.Join(t.TempDir(), "outside.go")}, false); err == nil {
		t.Error("packageToolArguments(outside file) error = nil, want package path validation error")
	}
	if _, err := toolArguments("repo", "unknown", []string{"unknown"}, []string{"a.go"}, nil, false); err == nil {
		t.Error("toolArguments(unknown tool) error = nil, want unsupported-tool error")
	}
}

func TestValidateToolExitAndToolPercentage(t *testing.T) {
	runExit := func(code string) error {
		t.Helper()
		cmd := exec.Command("sh", "-c", "exit "+code)
		return cmd.Run()
	}
	exit1 := runExit("1")
	exit2 := runExit("2")
	if err := validateToolExit("tool", "vet", nil, nil, nil); err != nil {
		t.Errorf("validateToolExit(success) = %v, want nil", err)
	}
	diagnostics := []FileSummary{{Filename: "main.go", Errors: []Error{{LineNumber: 1}}}}
	if err := validateToolExit("golint", "golint", exit1, []byte("warning"), diagnostics); err != nil {
		t.Errorf("validateToolExit(exit 1 with diagnostics) = %v, want nil", err)
	}
	if err := validateToolExit("misspell", "misspell", exit2, []byte("typo"), diagnostics); err != nil {
		t.Errorf("validateToolExit(misspell exit 2 with diagnostics) = %v, want nil", err)
	}
	for _, tc := range []struct {
		tool string
		err  error
	}{
		{"vet", exit1},
		{"golint", exit2},
		{"golint", exit1},
	} {
		if err := validateToolExit("tool", tc.tool, tc.err, []byte("failed"), nil); err == nil {
			t.Errorf("validateToolExit(%s, %v, no diagnostics) = nil, want failure", tc.tool, tc.err)
		}
	}

	filename := filepath.Join(t.TempDir(), "main.go")
	if err := os.WriteFile(filename, []byte("one\ntwo\nthree\nfour\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	percentage, summaries, err := toolPercentage([]string{filename}, diagnostics)
	if err != nil || percentage != .75 || len(summaries) != 1 {
		t.Errorf("toolPercentage(one file with one issue) = (%v, %v, %v), want (.75, one summary, nil)", percentage, summaries, err)
	}
	if _, _, err := toolPercentage([]string{filepath.Join(t.TempDir(), "missing.go")}, nil); err == nil {
		t.Error("toolPercentage(missing file) error = nil, want line-count error")
	}
	percentage, _, err = toolPercentage([]string{"a.go", "b.go"}, diagnostics)
	if err != nil || percentage != .5 {
		t.Errorf("toolPercentage(multiple files) = (%v, %v), want .5 and nil", percentage, err)
	}
}

func TestRunRecordsCheckFailures(t *testing.T) {
	dir := t.TempDir()
	filename := filepath.Join(dir, "main.go")
	if err := os.WriteFile(filename, []byte("package main\n\nfunc main() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	for _, name := range []string{"gofmt", "go", "gocyclo", "misspell", "ineffassign"} {
		writeExecutable(t, binDir, name, "#!/bin/sh\nexit 2\n")
	}
	t.Setenv("PATH", binDir)

	result, err := Run(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if !result.DidError {
		t.Error("Run() DidError = false, want true when analyzer commands fail")
	}
	failedChecks := 0
	for _, score := range result.Checks {
		if score.Error != "" {
			failedChecks++
		}
	}
	if failedChecks == 0 {
		t.Errorf("Run() returned no check errors: %#v", result.Checks)
	}
}

func TestRunContinuesWhenBackupFilenameIsTooLong(t *testing.T) {
	dir := t.TempDir()
	mainFile := filepath.Join(dir, "main.go")
	if err := os.WriteFile(mainFile, []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	longSkipped := filepath.Join(dir, strings.Repeat("a", 249)+".pb.go")
	if err := os.WriteFile(longSkipped, []byte("package main\n"), 0o600); err != nil {
		t.Skipf("filesystem does not support a 255-byte filename: %v", err)
	}
	binDir := t.TempDir()
	for _, name := range []string{"gofmt", "go", "gocyclo", "misspell", "ineffassign"} {
		writeExecutable(t, binDir, name, "#!/bin/sh\nexit 0\n")
	}
	writeExecutable(t, binDir, "wc", "#!/bin/sh\nprintf '1 %s\\n' \"$2\"\n")
	t.Setenv("PATH", binDir)

	result, err := Run(dir, false)
	if err != nil {
		t.Fatalf("Run() error = %v, want to continue after a skipped-file rename failure", err)
	}
	if result.DidError {
		t.Errorf("Run() marked analyzer checks as failed: %#v", result.Checks)
	}
	if _, err := os.Stat(longSkipped); err != nil {
		t.Errorf("Run() did not preserve skipped source after rename failure: %v", err)
	}

}

func TestRunPackageGroupsAggregatesResults(t *testing.T) {
	root := t.TempDir()
	moduleA := filepath.Join(root, "a")
	moduleB := filepath.Join(root, "b")
	for _, module := range []string{moduleA, moduleB} {
		if err := os.MkdirAll(module, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(module, "go.mod"), []byte("module example.com/"+filepath.Base(module)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(module, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	shared := filepath.Join(moduleA, "shared.go")
	if err := os.WriteFile(shared, []byte("package main\n\nfunc shared() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	binDir := t.TempDir()
	writeExecutable(t, binDir, "golint", "#!/bin/sh\nprintf '%s:1: shared issue\\n' \"$SHARED_FIXTURE\"\nprintf '%s/main.go:2: local issue\\n' \"$PWD\"\nexit 1\n")
	t.Setenv("SHARED_FIXTURE", shared)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	filesA := []string{shared, filepath.Join(moduleA, "main.go")}
	fileB := filepath.Join(moduleB, "main.go")
	groups := []packageGroup{{dir: moduleA, filenames: filesA}, {dir: moduleB, filenames: []string{fileB}}}
	percentage, summaries, err := runPackageGroups([]string{shared, filesA[1], fileB}, []string{"golint"}, groups)
	if err != nil {
		t.Fatal(err)
	}
	if percentage != 0 || len(summaries) != 3 {
		t.Fatalf("runPackageGroups() = (%v, %#v), want three unique failing files and 0 percent", percentage, summaries)
	}
	wantNames := []string{filepath.Join(moduleA, "main.go"), shared, filepath.Join(moduleB, "main.go")}
	for i, summary := range summaries {
		if summary.Filename != wantNames[i] {
			t.Errorf("summary[%d].Filename = %q, want %q", i, summary.Filename, wantNames[i])
		}
		if summary.Filename == shared && len(summary.Errors) != 2 {
			t.Errorf("shared summary has %d errors, want diagnostics merged across groups", len(summary.Errors))
		}
	}

	writeExecutable(t, binDir, "golint", "#!/bin/sh\nprintf 'malformed analyzer output\\n'\n")
	_, _, err = runPackageGroups([]string{filesA[1]}, []string{"golint"}, []packageGroup{{dir: moduleA, filenames: []string{filesA[1]}}})
	if err == nil || !strings.Contains(err.Error(), moduleA) {
		t.Errorf("runPackageGroups(malformed tool output) error = %v, want a group-scoped error", err)
	}
}

func writeExecutable(t *testing.T, dir, name, contents string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
}

func containsEnv(env []string, want string) bool {
	for _, value := range env {
		if value == want {
			return true
		}
	}
	return false
}
