package util

import (
	"fmt"
	"log"
	"os"
	"strings"
	"unicode"
)

var FileExtVideo = func() *Set[string] {
	s := NewSet[string]()
	for _, ext := range []string{
		".3g2",
		".3gp",
		".amv",
		".asf",
		".avi",
		".drc",
		".f4a",
		".f4b",
		".f4p",
		".f4v",
		".flv",
		".gif",
		".gifv",
		".m2ts",
		".m2v",
		".m4p",
		".m4v",
		".mk3d",
		".mkv",
		".mng",
		".mov",
		".mp2",
		".mp4",
		".mpe",
		".mpeg",
		".mpg",
		".mpv",
		".mxf",
		".nsv",
		".ogg",
		".ogm",
		".ogv",
		".qt",
		".rm",
		".rmvb",
		".roq",
		".svi",
		".ts",
		".webm",
		".wmv",
		".yuv",
	} {
		s.Add(ext)
	}
	return s
}()

var FileExtSubtitle = func() *Set[string] {
	s := NewSet[string]()
	for _, ext := range []string{
		".srt",
		".ass",
		".ssa",
		".sub",
	} {
		s.Add(ext)
	}
	return s
}()

func ParseDisallowedExtensions(value string) (*Set[string], error) {
	s := NewSet[string]()
	for _, ext := range []string{
		".exe",
		".com",
		".scr",
		".msi",
		".bat",
		".cmd",
		".ps1",
		".vbs",
		".js",
		".jar",
		".lnk",
		".sh",
		".bash",
		".zsh",
		".fish",
		".run",
		".appimage",
		".deb",
		".rpm",
		".app",
		".dmg",
		".pkg",
		".command",
		".scpt",
		".scptd",
		".aab",
		".apk",
		".apks",
		".apkm",
		".xapk",
	} {
		s.Add(ext)
	}
	// Adjust the defaults in order; the last adjustment for an extension wins.
	for _, adjustment := range strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || unicode.IsSpace(r)
	}) {
		if len(adjustment) < 2 || (adjustment[0] != '+' && adjustment[0] != '-') {
			return nil, fmt.Errorf("invalid extension adjustment %q: expected +ext or -ext", adjustment)
		}
		ext := strings.ToLower(strings.TrimPrefix(adjustment[1:], "."))
		if ext == "" {
			return nil, fmt.Errorf("invalid extension adjustment %q: empty extension", adjustment)
		}
		for _, r := range ext {
			if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
				return nil, fmt.Errorf("invalid extension adjustment %q: use a single extension containing only letters and digits", adjustment)
			}
		}
		ext = "." + ext
		if adjustment[0] == '-' {
			s.Del(ext)
		} else {
			s.Add(ext)
		}
	}
	return s, nil
}

var FileExtDisallowed = func() *Set[string] {
	extensions, err := ParseDisallowedExtensions(os.Getenv("STREMTHRU_DISALLOWED_EXTENSIONS"))
	if err != nil {
		log.Fatalf("invalid STREMTHRU_DISALLOWED_EXTENSIONS: %v", err)
	}
	return extensions
}()
