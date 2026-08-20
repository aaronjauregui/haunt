package seriko

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Licensing, which is the part of importing somebody else's art that
// matters most and is easiest to skip.
//
// Two decades of ukagaka shells exist and their terms vary from "do what
// you like" to "ask me first" to "no redistribution at all" — and the terms
// are nearly always in a readme, in Japanese, next to the art rather than
// inside it. An importer that quietly converts anything it is pointed at
// makes the person using it responsible for a decision they were never
// shown, usually at the moment they publish.
//
// So: the shell must carry a readme, the converter prints it, and it refuses
// to write anything until the person says they have read it. That is not
// legal advice and it is not a check the tool could pass on their behalf —
// it is making sure the question was asked while it could still be answered.

// licenseNames are the files a shell's terms are usually in.
var licenseNames = []string{
	"readme.txt", "readme.md", "read me.txt", "license.txt", "license",
	"licence.txt", "copying", "readme_e.txt", "readme.html", "利用規約.txt",
	"readme first.txt", "terms.txt",
}

// License is the terms found beside a shell.
type License struct {
	// File is the name it was found in.
	File string
	// Text is its content, already decoded from whatever the file used.
	Text string
}

// Restrictive reports the phrases in the text that suggest redistribution is
// not permitted, so the tool can say more than "here is a wall of text".
// It is a hint, deliberately not a verdict: matching nothing is not
// permission, and the caller is told so.
func (l License) Restrictive() []string {
	var found []string
	for phrase, why := range restrictivePhrases {
		if strings.Contains(l.Text, phrase) {
			found = append(found, why)
		}
	}
	sort.Strings(found)
	return uniq(found)
}

// restrictivePhrases are the ones that actually turn up in these readmes,
// in both languages. The Japanese matter more: most of these shells were
// never written about in English.
var restrictivePhrases = map[string]string{
	"転載":                  "mentions 転載 (reproduction/reposting)",
	"再配布":                 "mentions 再配布 (redistribution)",
	"二次配布":                "mentions 二次配布 (secondary distribution)",
	"無断":                  "mentions 無断 (without permission)",
	"禁止":                  "mentions 禁止 (prohibited)",
	"All rights reserved": "says All rights reserved",
	"all rights reserved": "says all rights reserved",
	"without permission":  "says without permission",
	"is prohibited":       "says is prohibited",
	"may not be":          "says may not be",
	"Do not redistribute": "says Do not redistribute",
	"do not redistribute": "says do not redistribute",
	"commercial use":      "mentions commercial use",
	"Reproduction":        "mentions Reproduction",
}

func uniq(in []string) []string {
	out := in[:0]
	var last string
	for i, s := range in {
		if i == 0 || s != last {
			out = append(out, s)
		}
		last = s
	}
	return out
}

// FindLicense looks for a shell's terms. The search is deliberately wide —
// a readme one directory up from surfaces.txt still governs the art — and
// reports what it looked at when it finds nothing, so the answer is
// actionable rather than a bare refusal.
func FindLicense(src Source) (License, bool) {
	for _, name := range licenseNames {
		if b, ok := src.Read(name); ok && len(b) > 0 {
			return License{File: name, Text: Decode(b)}, true
		}
	}
	return License{}, false
}

// ErrNoLicense is what the tool reports when a shell carries no terms.
type ErrNoLicense struct{ Looked []string }

func (e *ErrNoLicense) Error() string {
	return fmt.Sprintf("this shell carries no readme or licence file, so its terms are unknown "+
		"(looked for: %s).\nukagaka shells are nearly always someone's artwork under terms they "+
		"wrote down; converting one whose terms you cannot read is how you end up redistributing "+
		"art you had no permission to.\nIf you know the terms, pass -i-have-permission",
		strings.Join(e.Looked, ", "))
}

// LicenseNames is what FindLicense looks for, for the error message.
func LicenseNames() []string { return licenseNames }

// summary trims a licence to something a terminal can show without
// scrolling the important part off the top.
var blankLines = regexp.MustCompile(`\n{3,}`)

// Summary is the licence text tidied for printing.
func (l License) Summary(maxLines int) string {
	text := blankLines.ReplaceAllString(strings.ReplaceAll(l.Text, "\r\n", "\n"), "\n\n")
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) <= maxLines {
		return strings.Join(lines, "\n")
	}
	return strings.Join(lines[:maxLines], "\n") +
		fmt.Sprintf("\n… (%d more lines in %s)", len(lines)-maxLines, l.File)
}
