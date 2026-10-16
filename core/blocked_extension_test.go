package core

import (
	"strings"
	"testing"
)

func TestHasDisallowedExtension(t *testing.T) {
	for _, ext := range strings.Fields(".exe .com .scr .msi .bat .cmd .ps1 .vbs .js .jar .lnk .sh .bash .zsh .fish .run .appimage .deb .rpm .app .dmg .pkg .command .scpt .scptd .aab .apk .apks .apkm .xapk") {
		for _, filename := range []string{"file" + ext, "file" + strings.ToUpper(ext), "/folder/file" + ext, `C:\folder\file` + ext, "file" + ext + ".", "file" + ext + " "} {
			if !HasDisallowedExtension(filename) {
				t.Errorf("did not block %q", filename)
			}
		}
	}
	for _, filename := range []string{"", "movie.mkv", "subtitle.srt", "artwork.jpg", "readme.txt", "archive.zip", "movie.exe.mkv", "extensionless"} {
		if HasDisallowedExtension(filename) {
			t.Errorf("unexpectedly blocked %q", filename)
		}
	}
	for _, filename := range []string{"Example.app/Contents/MacOS/Example", `Example.APP\Contents\MacOS\Example`, "Example.scptd/Contents/script"} {
		if HasDisallowedExtension(filename) {
			t.Errorf("unexpectedly blocked bundle descendant %q", filename)
		}
	}
}

func TestHasDisallowedExtensionDirectoryNames(t *testing.T) {
	for _, ext := range strings.Fields(".exe .com .scr .msi .bat .cmd .ps1 .vbs .js .jar .lnk .sh .bash .zsh .fish .run .appimage .deb .rpm .dmg .pkg .command .scpt .aab .apk .apks .apkm .xapk") {
		for _, filename := range []string{"/example" + ext + "/movie.mkv", `C:\example` + strings.ToUpper(ext) + `\movie.mkv`, "/example" + ext + "/", `C:\example` + ext + `\`} {
			if HasDisallowedExtension(filename) {
				t.Errorf("unexpectedly blocked directory name in %q", filename)
			}
		}
	}
	for _, filename := range []string{"/Example.scptd/Contents/script", `C:\Example.SCPTD\Contents\script`, "/Example.app/Contents/movie.mkv", `C:\Example.APP\Contents\movie.mkv`} {
		if HasDisallowedExtension(filename) {
			t.Errorf("unexpectedly blocked bundle descendant %q", filename)
		}
	}
}

func TestIsAllowedVideoFile(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		allowed    bool
	}{
		{"movie.mkv", "/movie.mkv", true},
		{"movie.mkv.", "/movie.mkv.", true},
		{"movie.mkv ", "/movie.mkv ", true},
		{"", "/movie.mp4", true},
		{"", `C:\videos\movie.mp4.`, true},
		{"movie.mkv", "/Example.app/Contents/movie.mkv", true},
		{"movie.mkv", "/setup.exe", false},
		{"setup.exe", "/movie.mkv", false},
		{"subtitle.srt", "", false},
	} {
		if got := IsAllowedVideoFile(tc.name, tc.path); got != tc.allowed {
			t.Errorf("name=%q path=%q got=%v", tc.name, tc.path, got)
		}
	}
}
