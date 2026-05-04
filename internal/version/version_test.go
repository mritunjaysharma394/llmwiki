package version

import (
	"strings"
	"testing"
)

func TestFormatAllSet(t *testing.T) {
	saved := Version
	savedC := Commit
	savedD := BuildDate
	defer func() { Version, Commit, BuildDate = saved, savedC, savedD }()
	Version, Commit, BuildDate = "0.5.0-rc.1", "abc1234", "2026-05-04"
	got := Format()
	for _, want := range []string{"llmwiki", "0.5.0-rc.1", "abc1234", "2026-05-04"} {
		if !strings.Contains(got, want) {
			t.Errorf("Format() = %q, missing %q", got, want)
		}
	}
}

func TestFormatAllDevel(t *testing.T) {
	saved := Version
	savedC := Commit
	savedD := BuildDate
	defer func() { Version, Commit, BuildDate = saved, savedC, savedD }()
	Version, Commit, BuildDate = "(devel)", "(devel)", "(devel)"
	got := Format()
	if !strings.Contains(got, "(devel)") {
		t.Errorf("Format() = %q, want substring (devel)", got)
	}
}

func TestFormatPartial(t *testing.T) {
	saved := Version
	savedC := Commit
	savedD := BuildDate
	defer func() { Version, Commit, BuildDate = saved, savedC, savedD }()
	Version = "0.5.0"
	Commit = "(devel)"
	BuildDate = "(devel)"
	got := Format()
	if !strings.Contains(got, "0.5.0") {
		t.Errorf("Format() = %q, want substring 0.5.0", got)
	}
}
