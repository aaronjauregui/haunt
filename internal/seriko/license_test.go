package seriko

import (
	"strings"
	"testing"
)

// The rule the design asked for: refuse rather than make the person think
// about it later, when "later" means after they have published someone
// else's artwork.
func TestFindLicense(t *testing.T) {
	src := mem{"readme.txt": []byte("Copyright (c) 2012 someone. All rights reserved.")}
	lic, ok := FindLicense(src)
	if !ok {
		t.Fatal("a readme.txt was not found")
	}
	if lic.File != "readme.txt" || !strings.Contains(lic.Text, "Copyright") {
		t.Errorf("licence = %+v", lic)
	}
}

func TestFindLicenseMissing(t *testing.T) {
	if _, ok := FindLicense(mem{"surfaces.txt": []byte("surface0\n{\n}\n")}); ok {
		t.Error("a shell with no readme reported one")
	}
}

// The readmes that matter are in Japanese: most of these shells were never
// written about in English, and an importer that only reads English terms
// would wave through exactly the ones it should stop.
func TestRestrictiveJapanese(t *testing.T) {
	lic := License{Text: "・本ゴーストの構成ファイルを転用・改変・二次配布する場合は、予め作者に確認を取ってください。"}
	flags := lic.Restrictive()
	if len(flags) == 0 {
		t.Fatal("Japanese redistribution terms were not noticed")
	}
	joined := strings.Join(flags, " ")
	if !strings.Contains(joined, "二次配布") {
		t.Errorf("flags = %v", flags)
	}
}

func TestRestrictiveEnglish(t *testing.T) {
	lic := License{Text: "Copyright 2012. All rights reserved. Reproduction without permission is prohibited."}
	flags := lic.Restrictive()
	if len(flags) < 2 {
		t.Errorf("flags = %v, want several", flags)
	}
}

// Matching nothing is not permission, and the tool says so elsewhere; here
// it only has to not invent a flag.
func TestPermissiveLicenseIsNotFlagged(t *testing.T) {
	lic := License{Text: "Do whatever you like with this. Public domain."}
	if flags := lic.Restrictive(); len(flags) != 0 {
		t.Errorf("a permissive licence was flagged: %v", flags)
	}
}

func TestLicenseSummaryTrims(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 100; i++ {
		b.WriteString("line\n")
	}
	lic := License{File: "readme.txt", Text: b.String()}
	got := lic.Summary(10)
	if strings.Count(got, "\n") > 11 {
		t.Errorf("summary was not trimmed:\n%s", got)
	}
	if !strings.Contains(got, "more lines in readme.txt") {
		t.Errorf("the summary does not say what it cut: %q", got)
	}
	short := License{Text: "one line"}
	if short.Summary(10) != "one line" {
		t.Error("a short licence should be shown whole")
	}
}

func TestErrNoLicenseSaysWhatItLookedFor(t *testing.T) {
	err := &ErrNoLicense{Looked: []string{"readme.txt", "license.txt"}}
	msg := err.Error()
	for _, want := range []string{"readme.txt", "license.txt", "-i-have-permission"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal does not mention %q:\n%s", want, msg)
		}
	}
}
