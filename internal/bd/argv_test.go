package bd

import (
	"context"
	"reflect"
	"testing"

	"github.com/McBrideMusings/admin-project-tool/internal/argvscan"
)

// call is one recorded bd invocation. The directory is part of it: bd resolves
// its database from the working directory, so a command run in the wrong one
// reads the wrong workspace even with a perfect argv.
type call struct {
	dir  string
	args []string
}

// recorder stands in for the runner var: it records every call and replays
// canned stdout, so a test can assert the exact arguments a command function
// built without a `bd` binary or a beads workspace.
type recorder struct {
	calls []call
	out   []string // stdout for call i; calls past the end get "{}"
}

func (r *recorder) run(_ context.Context, dir string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, call{dir: dir, args: append([]string(nil), args...)})
	if i := len(r.calls) - 1; i < len(r.out) {
		return []byte(r.out[i]), nil
	}
	return []byte("{}"), nil
}

// TestCommandArgv pins the argv of every exported command in this package.
// #18 was a missing `--repo` on `gh pr list --state merged` in the sibling
// package: gh resolved the repo from the working directory, so in a fork the
// Cleanup box struck a live branch through as merged. A dropped `--json`, a
// `-n 0` that stops being passed or a changed filter here is the same class of
// mistake, and nothing else in the suite would see it.
//
// The returned values and errors are deliberately ignored — the canned stdout
// only has to get each function as far as its command call.
func TestCommandArgv(t *testing.T) {
	tests := []struct {
		name string
		out  []string
		call func(ctx context.Context)
		want []call
	}{
		{
			name: "Detect",
			out:  []string{`{"path":"/w","database_path":"/w/.beads/b.db","prefix":"x","schema_version":1}`},
			call: func(ctx context.Context) { Detect(ctx, "/repo") },
			want: []call{{dir: "/repo", args: []string{"where", "--json"}}},
		},
		{
			name: "List asks for every row, not bd's default 50",
			out:  []string{`[]`},
			call: func(ctx context.Context) { List(ctx, "/repo") },
			want: []call{{dir: "/repo", args: []string{"list", "--json", "-n", "0", "--no-pager"}}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &recorder{out: tt.out}
			restore := runner
			runner = rec.run
			defer func() { runner = restore }()

			tt.call(context.Background())

			if len(rec.calls) != len(tt.want) {
				t.Fatalf("made %d bd calls, want %d:\ngot  %v\nwant %v", len(rec.calls), len(tt.want), rec.calls, tt.want)
			}
			for i := range tt.want {
				if rec.calls[i].dir != tt.want[i].dir {
					t.Errorf("call %d ran in %q, want %q", i, rec.calls[i].dir, tt.want[i].dir)
				}
				if !reflect.DeepEqual(rec.calls[i].args, tt.want[i].args) {
					t.Errorf("call %d argv mismatch:\ngot  %q\nwant %q", i, rec.calls[i].args, tt.want[i].args)
				}
			}
		})
	}
}

// coveredCommands is every exported function in this package that runs a bd
// command. TestArgvCoverage fails when bd.go grows one that isn't listed, so a
// new command can't ship without its argv pinned above.
var coveredCommands = map[string]bool{
	"Detect": true,
	"List":   true,
}

func TestArgvCoverage(t *testing.T) {
	argvscan.Check(t, argvscan.Config{
		Tool:    "bd",
		Files:   []string{"bd.go"},
		Seams:   []string{"runner("},
		Covered: coveredCommands,
	})
}
