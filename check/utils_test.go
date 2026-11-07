package check

import (
	"bufio"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestGoFiles(t *testing.T) {
	files, skipped, err := GoFiles("testdata/testfiles/")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"testdata/testfiles/a.go", "testdata/testfiles/b.go", "testdata/testfiles/c.go", "testdata/testfiles/d.go"}
	if !reflect.DeepEqual(files, want) {
		t.Errorf("GoFiles(%q) = %v, want %v", "testdata/testfiles/", files, want)
	}

	wantSkipped := []string{"testdata/testfiles/a.pb.go", "testdata/testfiles/a.pb.gw.go"}
	if !reflect.DeepEqual(skipped, wantSkipped) {
		t.Errorf("GoFiles(%q) skipped = %v, want %v", "testdata/testfiles/", skipped, wantSkipped)
	}
}

func TestGoFilesSkipsSymlinks(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "outside.go")
	if err := os.WriteFile(target, []byte("package outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "linked.go")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	files, _, err := GoFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Errorf("GoFiles(%q) = %v, want symlink to be excluded", dir, files)
	}
}

var goToolTests = []struct {
	name      string
	dir       string
	filenames []string
	tool      []string
	percent   float64
	failed    []FileSummary
	wantErr   bool
}{
	{"go vet", "testdata/testfiles", []string{"testdata/testfiles/a.go", "testdata/testfiles/b.go", "testdata/testfiles/c.go"}, []string{"go", "vet"}, 1, []FileSummary{}, false},
	{
		"staticcheck",
		"testdata/testfiles/",
		[]string{"testdata/testfiles/a.go", "testdata/testfiles/b.go", "testdata/testfiles/c.go", "testdata/testfiles/d.go"},
		[]string{"staticcheck", "./..."},
		0.75,
		[]FileSummary{
			{
				Filename: "testdata/testfiles/d.go", FileURL: "",
				Errors: []Error{
					{LineNumber: 8, ErrorString: " func foo is unused (U1000)"},
					{LineNumber: 10, ErrorString: " should use time.Until instead of t.Sub(time.Now()) (S1024)"}},
			},
		},
		false,
	},
}

func TestGoTool(t *testing.T) {
	for _, tt := range goToolTests {
		f, fs, err := GoTool(tt.dir, tt.filenames, tt.tool)
		if err != nil && !tt.wantErr {
			t.Fatal(err)
		}
		if f != tt.percent {
			t.Errorf("[%s] GoTool percent = %f, want %f", tt.name, f, tt.percent)
		}
		if !reflect.DeepEqual(fs, tt.failed) {
			t.Errorf("[%s] GoTool failed = %#v, want %v", tt.name, fs, tt.failed)
		}
	}
}

func TestGoToolSupportsNestedWorkspaceModules(t *testing.T) {
	t.Setenv("GOWORK", "")
	root := t.TempDir()
	modulesDir := filepath.Join(root, "modules")
	if err := os.MkdirAll(modulesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	goWork := "go 1.20\n\nuse (\n\t./api\n\t./auth\n\t./inactive\n)\n"
	if err := os.WriteFile(filepath.Join(modulesDir, "go.work"), []byte(goWork), 0o600); err != nil {
		t.Fatal(err)
	}

	var filenames []string
	for _, module := range []string{"api", "auth", "inactive"} {
		moduleDir := filepath.Join(modulesDir, module)
		if err := os.MkdirAll(moduleDir, 0o755); err != nil {
			t.Fatal(err)
		}
		goMod := "module example.com/" + module + "\n\ngo 1.20\n"
		source := "package " + module + "\n\nfunc Exported() {}\n"
		if module == "auth" {
			goMod += "\nrequire example.com/api v0.0.0\n"
			source = "package auth\n\nimport \"example.com/api\"\n\nfunc UseAPI() { api.Exported() }\n"
		}
		if module == "inactive" {
			source = "//go:build ignore\n\npackage inactive\n"
		}
		if err := os.WriteFile(filepath.Join(moduleDir, "go.mod"), []byte(goMod), 0o600); err != nil {
			t.Fatal(err)
		}
		filename := filepath.Join(moduleDir, module+".go")
		if err := os.WriteFile(filename, []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
		filenames = append(filenames, filename)
	}

	percentage, failed, err := GoTool(root, filenames, []string{"go", "vet"})
	if err != nil {
		t.Fatal(err)
	}
	if percentage != 1 {
		t.Errorf("GoTool percentage = %v, want 1", percentage)
	}
	if len(failed) != 0 {
		t.Errorf("GoTool failures = %v, want none", failed)
	}
}

func TestGoToolSkipsInactiveOnlyPackage(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/inactive\n\ngo 1.27.3\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	filename := filepath.Join(dir, "ignored.go")
	source := []byte("//go:build ignore\n\npackage ignored\n")
	if err := os.WriteFile(filename, source, 0o600); err != nil {
		t.Fatal(err)
	}

	filenames, _, err := GoFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(filenames, []string{filename}) {
		t.Fatalf("GoFiles(%q) = %v, want the inactive source file to remain available to file checks", dir, filenames)
	}

	percentage, failed, err := GoTool(dir, filenames, []string{"go", "vet"})
	if err != nil {
		t.Fatal(err)
	}
	if percentage != 1 {
		t.Errorf("GoTool percentage = %v, want 1", percentage)
	}
	if len(failed) != 0 {
		t.Errorf("GoTool failures = %v, want none", failed)
	}
}

func TestMakeFilename(t *testing.T) {
	cases := []struct {
		fn   string
		want string
	}{
		{"/github.com/foo/bar/baz.go", "bar/baz.go"},
	}

	for _, tt := range cases {
		if got := makeFilename(tt.fn); got != tt.want {
			t.Errorf("makeFilename(%q) = %q, want %q", tt.fn, got, tt.want)
		}
	}
}

func TestFileURL(t *testing.T) {
	cases := []struct {
		fn   string
		want string
	}{
		{"/github.com/foo/testrepo@v0.1.0/bar/baz.go", "https://github.com/foo/testrepo/blob/v0.1.0/bar/baz.go"},
		{"/github-run/github.com/lbenicio/kubernetes-dashboard@0123456789abcdef0123456789abcdef01234567/modules/api/main.go", "https://github.com/lbenicio/kubernetes-dashboard/blob/0123456789abcdef0123456789abcdef01234567/modules/api/main.go"},
		{"/github.com/foo/testrepo@v0.0.0-20211126063219-a5e10ccf946a/bar/baz.go", "https://github.com/foo/testrepo/blob/a5e10ccf946a/bar/baz.go"},
		{"/github.com/foo/testrepo@v20.10.11+incompatible/bar/baz.go", "https://github.com/foo/testrepo/blob/v20.10.11/bar/baz.go"},
		{"/github.com/foo/testrepo@v0.1.0-alpha/bar/baz.go", "https://github.com/foo/testrepo/blob/v0.1.0-alpha/bar/baz.go"},
	}

	for _, tt := range cases {
		if got := fileURL(tt.fn); got != tt.want {
			t.Errorf("fileURL(%q) = %q, want %q", tt.fn, got, tt.want)
		}
	}

}

func TestDisplayFilename(t *testing.T) {
	cases := []struct {
		fn   string
		want string
	}{
		{"foo@v0.1.0/bar/baz.go", "bar/baz.go"},
		{"foo@v0.1.0/a/b/c/d/baz.go", "a/b/c/d/baz.go"},
	}

	for _, tt := range cases {
		if got := displayFilename(tt.fn); got != tt.want {
			t.Errorf("displayFilename(%q) = %q, want %q", tt.fn, got, tt.want)
		}
	}
}

func TestParseDiagnostic(t *testing.T) {
	cases := []struct {
		input string
		file  string
		line  int
		msg   string
	}{
		{"pkg/file.go:12:3: message", "pkg/file.go", 12, " message"},
		{"file.go:8: message", "file.go", 8, " message"},
	}

	for _, tt := range cases {
		file, line, msg, ok := parseDiagnostic(tt.input)
		if !ok || file != tt.file || line != tt.line || msg != tt.msg {
			t.Errorf("parseDiagnostic(%q) = (%q, %d, %q, %t), want (%q, %d, %q, true)", tt.input, file, line, msg, ok, tt.file, tt.line, tt.msg)
		}
	}
}

func TestNormalizeGocycloOutput(t *testing.T) {
	got, err := normalizeGocycloOutput("21 check GoTool check/utils.go:337:1")
	if err != nil {
		t.Fatal(err)
	}
	want := "check/utils.go:337:1: cyclomatic complexity 21 in function GoTool (gocyclo)"
	if got != want {
		t.Errorf("normalizeGocycloOutput() = %q, want %q", got, want)
	}
}

func TestPackageDirectories(t *testing.T) {
	got, err := packageDirectories("repo", []string{"repo/sub/b.go", "repo/a.go", "repo/sub/c.go"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{".", "./sub"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("packageDirectories() = %v, want %v", got, want)
	}
}

func TestGetFileSummaryMapIgnoresModuleDownloadProgress(t *testing.T) {
	dir := t.TempDir()
	filename := filepath.Join(dir, "main.go")
	if err := os.WriteFile(filename, []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	output := "go: downloading github.com/prometheus/client_golang v1.24.0\nmain.go:1: vet diagnostic\n"
	summaries, err := getFileSummaryMap(bufio.NewScanner(strings.NewReader(output)), dir, "vet")
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 {
		t.Fatalf("summaries = %#v, want one vet diagnostic", summaries)
	}
	for _, summary := range summaries {
		if len(summary.Errors) != 1 || summary.Errors[0].ErrorString != " vet diagnostic" {
			t.Errorf("summary = %#v, want the vet diagnostic without download progress", summary)
		}
	}
}

func TestGoVetReportsDiagnostics(t *testing.T) {
	dir := t.TempDir()
	goMod := []byte("module example.com/vetfixture\n\ngo 1.27.3\n")
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), goMod, 0o600); err != nil {
		t.Fatal(err)
	}

	filename := filepath.Join(dir, "main.go")
	source := []byte("package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"invalid %s\")\n}\n")
	if err := os.WriteFile(filename, source, 0o600); err != nil {
		t.Fatal(err)
	}

	_, summaries, err := (GoVet{Dir: dir, Filenames: []string{filename}}).Percentage()
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 || len(summaries[0].Errors) == 0 {
		t.Fatalf("GoVet summaries = %#v, want a diagnostic for the fixture", summaries)
	}
}
