package manifest

import "testing"

func TestIterEnvRefs(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []EnvRef
	}{
		{
			name: "no refs",
			in:   "plain string, no vars",
			want: nil,
		},
		{
			name: "simple ref no default",
			in:   "hello ${NAME} world",
			want: []EnvRef{{Start: 6, End: 13, Name: "NAME"}},
		},
		{
			name: "ref with plain default",
			in:   "${PORT:-8080}",
			want: []EnvRef{{Start: 0, End: 13, Name: "PORT", Default: "8080", HasDefault: true}},
		},
		{
			name: "nested default is not truncated at the first close brace",
			in:   "${GOPATH_BIN:-${HOME}/go/bin}",
			want: []EnvRef{{Start: 0, End: 29, Name: "GOPATH_BIN", Default: "${HOME}/go/bin", HasDefault: true}},
		},
		{
			name: "unbalanced brace yields nothing further",
			in:   "prefix ${UNCLOSED and more text",
			want: nil,
		},
		{
			name: "shell-style construct with no valid name is skipped",
			in:   "${x%.c}",
			want: nil,
		},
		{
			name: "two refs in sequence",
			in:   "${A}-${B:-b}",
			want: []EnvRef{
				{Start: 0, End: 4, Name: "A"},
				{Start: 5, End: 12, Name: "B", Default: "b", HasDefault: true},
			},
		},
		{
			name: "empty string",
			in:   "",
			want: nil,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := IterEnvRefs(c.in)
			if len(got) != len(c.want) {
				t.Fatalf("IterEnvRefs(%q) = %#v, want %#v", c.in, got, c.want)
			}
			for i := range got {
				g, w := got[i], c.want[i]
				if g.Start != w.Start || g.End != w.End || g.Name != w.Name ||
					g.Default != w.Default || g.HasDefault != w.HasDefault {
					t.Errorf("IterEnvRefs(%q)[%d] = %+v, want %+v", c.in, i, g, w)
				}
			}
		})
	}
}
