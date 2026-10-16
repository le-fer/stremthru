package util

import (
	"strings"
	"testing"
)

func TestParseDisallowedExtensions(t *testing.T) {
	for _, tc := range []struct {
		value string
		exe   bool
		pdf   bool
	}{
		{"", true, false},
		{"-exe +pdf", false, true},
		{"-.EXE,+.PDF", false, true},
		{" -exe,\t+pdf\n+exe ", true, true},
		{"+pdf -pdf", true, false},
		{"-exe -exe +pdf +pdf", false, true},
	} {
		t.Run(tc.value, func(t *testing.T) {
			extensions, err := ParseDisallowedExtensions(tc.value)
			if err != nil {
				t.Fatal(err)
			}
			if extensions.Has(".exe") != tc.exe || extensions.Has(".pdf") != tc.pdf {
				t.Fatalf("unexpected adjustments: exe=%v pdf=%v", extensions.Has(".exe"), extensions.Has(".pdf"))
			}
			for _, ext := range strings.Fields(".com .scr .msi .bat .cmd .ps1 .vbs .js .jar .lnk .sh .bash .zsh .fish .run .appimage .deb .rpm .app .dmg .pkg .command .scpt .scptd .aab .apk .apks .apkm .xapk") {
				if !extensions.Has(ext) {
					t.Errorf("unrelated default %s was removed", ext)
				}
			}
		})
	}
}

func TestParseDisallowedExtensionsRejectsInvalidInput(t *testing.T) {
	for _, value := range []string{"exe", "+", "-", "+.", "+..exe", "+tar.gz", "+folder/exe", `+folder\exe`, "+*.exe", "+-exe"} {
		t.Run(value, func(t *testing.T) {
			if _, err := ParseDisallowedExtensions(value); err == nil {
				t.Fatalf("accepted invalid adjustment %q", value)
			}
		})
	}
}
