package manifest

import (
	"strings"
	"testing"
	"time"
)

func TestParseStatusAllThreeKinds(t *testing.T) {
	dir := writeManifest(t, `
project_name = "statustest"

[status.prod]
kind = "http"
url  = "${PROD_URL}"

[status.jobs]
kind    = "json"
url     = "${PROD_URL}/api/queue"
headers = { Authorization = "Bearer ${TOKEN}" }
every   = "30s"
pull    = { running = "length([?status=='transcoding'])" }
show    = "{running} running"

[status.disk]
kind = "command"
run  = "df -h /"
`)
	m, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := m.Status["prod"]; got.Kind != StatusHTTP || got.URL != "${PROD_URL}" {
		t.Errorf("prod = %+v", got)
	}
	jobs := m.Status["jobs"]
	if jobs.Every != 30*time.Second || jobs.Headers["Authorization"] != "Bearer ${TOKEN}" || jobs.Pull["running"] == "" || jobs.Show != "{running} running" {
		t.Errorf("jobs = %+v", jobs)
	}
	if got := m.Status["disk"]; got.Run != "df -h /" {
		t.Errorf("disk = %+v", got)
	}
	if _, leaked := m.Settings["status"]; leaked {
		t.Error("[status] leaked into Settings")
	}
	if len(m.Unknown) != 0 {
		t.Errorf("Unknown = %v, want none", m.Unknown)
	}
}

func TestStatusIntervalDefaultsByKind(t *testing.T) {
	if got := (StatusSpec{Kind: StatusHTTP}).Interval(); got != 5*time.Second {
		t.Errorf("http default = %v", got)
	}
	if got := (StatusSpec{Kind: StatusJSON}).Interval(); got != 15*time.Second {
		t.Errorf("json default = %v", got)
	}
	if got := (StatusSpec{Kind: StatusJSON, Every: time.Minute}).Interval(); got != time.Minute {
		t.Errorf("explicit every = %v", got)
	}
}

func TestParseStatusRejectsBadEntries(t *testing.T) {
	cases := map[string]struct{ body, want string }{
		"no kind":          {`[status.a]` + "\n" + `url = "http://x"`, "needs a kind"},
		"unknown kind":     {`[status.a]` + "\n" + `kind = "ping"`, `unknown kind "ping"`},
		"http without url": {`[status.a]` + "\n" + `kind = "http"`, "needs a url"},
		"json no show":     {`[status.a]` + "\n" + `kind = "json"` + "\n" + `url = "http://x"` + "\n" + `pull = { n = "length(@)" }`, "needs url, pull and show"},
		"command with url": {`[status.a]` + "\n" + `kind = "command"` + "\n" + `run = "true"` + "\n" + `url = "http://x"`, "takes only run and every"},
		"unknown key":      {`[status.a]` + "\n" + `kind = "http"` + "\n" + `url = "http://x"` + "\n" + `urls = "x"`, `unknown key "urls"`},
		"short every":      {`[status.a]` + "\n" + `kind = "http"` + "\n" + `url = "http://x"` + "\n" + `every = "100ms"`, "at least 1s"},
		"bad pull name":    {`[status.a]` + "\n" + `kind = "json"` + "\n" + `url = "http://x"` + "\n" + `pull = { "job-count" = "length(@)" }` + "\n" + `show = "x"`, "letters, digits and underscores"},
		"not a table":      {`status = "x"`, "'status' must be a table"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := writeManifest(t, "project_name = \"x\"\n"+tc.body+"\n")
			_, err := Load(dir)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}
