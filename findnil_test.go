package findnil_test

import (
	"bytes"
	"flag"
	"path/filepath"
	"testing"

	"github.com/gostaticanalysis/findnil"
	"github.com/tenntenn/golden"
)

var (
	flagUpdate bool
)

func init() {
	flag.BoolVar(&flagUpdate, "update", false, "update golden files")
}

func TestCmd_Run(t *testing.T) {
	t.Parallel()
	cases := []struct {
		pkg          string
		wantExitcode int
	}{
		{"a", findnil.ExitSuccess},
	}

	for _, tt := range cases {
		tt := tt
		t.Run(tt.pkg, func(t *testing.T) {
			t.Parallel()
			var stdout, stderr bytes.Buffer
			cmd := &findnil.Cmd{
				Dir:    filepath.Join("testdata", tt.pkg),
				Stdout: &stdout,
				Stderr: &stderr,
			}

			got := cmd.Run("./...")
			if got != tt.wantExitcode {
				t.Fatalf("exitcode: want %d, got %d with %s", tt.wantExitcode, got, &stderr)
			}

			testdata := filepath.Join("testdata", "golden")
			if flagUpdate {
				golden.Update(t, testdata, tt.pkg, &stdout)
				return
			}

			if diff := golden.Diff(t, testdata, tt.pkg, &stdout); diff != "" {
				t.Error(diff)
			}
		})
	}
}
