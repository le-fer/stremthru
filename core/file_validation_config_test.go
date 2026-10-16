package core

import (
	"testing"

	"github.com/MunifTanjim/stremthru/internal/util"
)

func TestDisallowedExtensionAdjustments(t *testing.T) {
	extensions, err := util.ParseDisallowedExtensions("-exe +pdf -app -scptd")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		filename string
		blocked  bool
	}{
		{"/setup.EXE", false},
		{"/readme.PDF", true},
		{"/Example.app/Contents/movie.mkv", false},
		{`C:\Example.SCPTD\Contents\script`, false},
		{"/setup.sh", true},
		{"/example.com/movie.mkv", false},
	} {
		t.Run(tc.filename, func(t *testing.T) {
			if got := hasDisallowedExtension(tc.filename, extensions); got != tc.blocked {
				t.Fatalf("blocked=%v; want %v", got, tc.blocked)
			}
		})
	}
}

func TestDisallowedBundleAdjustmentOrder(t *testing.T) {
	extensions, err := util.ParseDisallowedExtensions("-app +app -scptd +scptd")
	if err != nil {
		t.Fatal(err)
	}
	for _, filename := range []string{"/Example.app", `C:\Example.SCPTD`} {
		if !hasDisallowedExtension(filename, extensions) {
			t.Errorf("did not block re-enabled bundle %q", filename)
		}
	}
}
