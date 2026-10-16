package torrent_stream

import "testing"

func TestHasDisallowedFile(t *testing.T) {
	for _, tc := range []struct {
		name    string
		files   Files
		blocked bool
	}{
		{"empty", nil, false},
		{"video and subtitles", Files{{Name: "movie.mkv", Path: "/movie.mkv"}, {Name: "subtitle.srt", Path: "/subtitle.srt"}}, false},
		{"blocked name", Files{{Name: "setup.exe", Path: "/movie.mkv"}}, true},
		{"blocked path", Files{{Name: "movie.mkv", Path: "/setup.PKG"}}, true},
		{"video before script", Files{{Path: "/movie.mkv"}, {Name: "setup.sh"}}, true},
		{"script before video", Files{{Name: "setup.ps1"}, {Path: "/movie.mkv"}}, true},
		{"app bundle", Files{{Name: "movie.mkv", Path: "/Example.app/Contents/movie.mkv"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.files.HasDisallowedFile(); got != tc.blocked {
				t.Fatalf("blocked=%v; want %v", got, tc.blocked)
			}
		})
	}
}

func TestHasVideoPreservesPathBasedStorageCheck(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files Files
		video bool
	}{
		{"empty", nil, false},
		{"video name without video path", Files{{Name: "movie.MKV", Path: "/folder"}}, false},
		{"video path fallback", Files{{Path: "/movie.MP4"}}, true},
		{"nonvideo", Files{{Name: "readme.txt", Path: "/readme.txt"}}, false},
		{"video path with nonvideo name", Files{{Name: "readme.txt", Path: "/movie.mkv"}}, true},
		{"blocked video path remains video evidence", Files{{Name: "setup.exe", Path: "/movie.mkv"}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.files.HasVideo(); got != tc.video {
				t.Fatalf("HasVideo=%v; want %v", got, tc.video)
			}
		})
	}
}
