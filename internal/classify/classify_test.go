package classify

import "testing"

func TestClassify(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    Kind
		number  int
		nwo     string
		wantTxt string
	}{
		{"pr url", "https://github.com/owner/repo/pull/123", KindPR, 123, "owner/repo", ""},
		{"pr url with trailing path", "https://github.com/owner/repo/pull/7/files", KindPR, 7, "owner/repo", ""},
		{"issue url", "https://github.com/owner/repo/issues/45", KindIssue, 45, "owner/repo", ""},
		{"project board url", "https://github.com/orgs/Org/projects/1/views/2?issue=Org|repo|45", KindIssue, 45, "Org/repo", ""},
		{"bare number", "42", KindNumber, 42, "", ""},
		{"hash number", "#42", KindNumber, 42, "", ""},
		{"branch name", "feat/thing", KindText, 0, "", "feat/thing"},
		{"plain name", "my-feature", KindText, 0, "", "my-feature"},
		{"project url without issue param", "https://github.com/orgs/Org/projects/1", KindText, 0, "", "https://github.com/orgs/Org/projects/1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Classify(tt.input)
			if got.Kind != tt.want {
				t.Errorf("Kind = %v, want %v", got.Kind, tt.want)
			}
			if got.Number != tt.number {
				t.Errorf("Number = %d, want %d", got.Number, tt.number)
			}
			if got.NWO != tt.nwo {
				t.Errorf("NWO = %q, want %q", got.NWO, tt.nwo)
			}
			if tt.want == KindText && got.Text != tt.wantTxt {
				t.Errorf("Text = %q, want %q", got.Text, tt.wantTxt)
			}
		})
	}
}
