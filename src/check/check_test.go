package check

import (
	"testing"
)

func TestRun(t *testing.T) {
	cr, err := Run("testdata/testrepo@v0.1.0", false)
	if err != nil {
		t.Fatal(err)
	}

	if cr.Issues != 2 {
		t.Errorf("got cr.Issues = %d, want %d", cr.Issues, 2)
	}

	checks := make(map[string]Score, len(cr.Checks))
	for _, score := range cr.Checks {
		checks[score.Name] = score
	}
	for _, name := range []string{"gofmt", "misspell"} {
		if len(checks[name].FileSummaries) == 0 {
			t.Errorf("expected %s to report the fixture issue", name)
		}
	}
}
