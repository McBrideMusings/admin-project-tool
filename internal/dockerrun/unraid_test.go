package dockerrun

import (
	"strings"
	"testing"
)

func TestUnraidTemplateMapsEveryArgvPiece(t *testing.T) {
	argv := []string{
		"-v", "/mnt/user/appdata/app:/data",
		"-v", "/mnt/user/media:/media:ro",
		"-v", "/cache",
		"-p", "100.64.0.1:5194:5194/tcp",
		"-p", "8080:80/tcp",
		"-e", "PLEX_URL=http://10.0.0.2:32400",
		"-e", "PLEX_TOKEN=s3cr<et",
		"--restart", "unless-stopped",
		"--gpus", "all",
		"--label", "net.unraid.docker.managed=dockerman",
		"--label", "com.example.x=y",
	}
	got := UnraidTemplate("plexdb", "plexdb:latest", argv)

	for _, want := range []string{
		`<Name>plexdb</Name>`,
		`<Repository>plexdb:latest</Repository>`,
		`<Network>bridge</Network>`,
		`<ExtraParams>-v /cache --restart unless-stopped --gpus all</ExtraParams>`,
		`Target="/data" Default="" Mode="rw" Description="" Type="Path" Display="always" Required="false" Mask="false">/mnt/user/appdata/app</Config>`,
		`Target="/media" Default="" Mode="ro" Description="" Type="Path"`,
		`Target="5194" Default="" Mode="tcp" Description="" Type="Port" Display="always" Required="false" Mask="false">100.64.0.1:5194</Config>`,
		`Target="80" Default="" Mode="tcp" Description="" Type="Port" Display="always" Required="false" Mask="false">8080</Config>`,
		`Target="PLEX_URL" Default="" Mode="" Description="" Type="Variable" Display="always" Required="false" Mask="false">http://10.0.0.2:32400</Config>`,
		`Target="PLEX_TOKEN" Default="" Mode="" Description="" Type="Variable" Display="always" Required="false" Mask="true">s3cr&lt;et</Config>`,
		`Target="com.example.x" Default="" Mode="" Description="" Type="Label" Display="always" Required="false" Mask="false">y</Config>`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("template missing %q\n%s", want, got)
		}
	}
	if strings.Contains(got, unraidManagedLabel) {
		t.Errorf("dockerMan adds %s itself; the template must not list it\n%s", unraidManagedLabel, got)
	}
	if strings.Count(got, `Type="Label"`) != 1 {
		t.Errorf("want exactly one Label config\n%s", got)
	}
}

func TestUnraidTemplateNetworkAndQuoting(t *testing.T) {
	got := UnraidTemplate("c", "img", []string{"--network", "proxy", "--entrypoint", "sh -c 'x'", "--privileged", "-t", "--hostname", "-e", "-e", "A=1"})
	if !strings.Contains(got, `<Network>proxy</Network>`) {
		t.Errorf("network not carried\n%s", got)
	}
	if !strings.Contains(got, `<Privileged>true</Privileged>`) {
		t.Errorf("--privileged not carried as <Privileged>\n%s", got)
	}
	if strings.Count(got, `Type="Variable"`) != 1 {
		t.Errorf("--hostname's value was read as a flag\n%s", got)
	}
	if !strings.Contains(got, `<ExtraParams>--entrypoint &#39;sh -c &#39;\&#39;&#39;x&#39;\&#39;&#39;&#39; -t --hostname -e</ExtraParams>`) {
		t.Errorf("extra param not shell-quoted then XML-escaped\n%s", got)
	}
}
