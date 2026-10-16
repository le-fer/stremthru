package stremio_shared

import (
	"fmt"
	"slices"
	"testing"

	"github.com/MunifTanjim/stremthru/internal/torrent_stream"
	"github.com/MunifTanjim/stremthru/internal/util"
	"github.com/MunifTanjim/stremthru/store"
	"github.com/stretchr/testify/require"
)

func TestValidatePlaybackFiles(t *testing.T) {
	for _, allow := range []bool{false, true} {
		for _, tc := range []struct {
			name     string
			files    []store.MagnetFile
			fileName string
			fileIdx  int
			code     store.StoreCode
			reason   string
		}{
			{"video and subtitle", []store.MagnetFile{{Name: "movie.mkv", Path: "/movie.mkv"}, {Name: "subtitle.srt"}}, "", -1, store.StoreCodeTorBox, ""},
			{"bundle descendant", []store.MagnetFile{{Name: "movie.mkv", Path: "/Example.app/Contents/movie.mkv"}}, "", -1, store.StoreCodeTorBox, ""},
			{"empty", nil, "", -1, store.StoreCodeTorBox, "torrent contains no allowed video files"},
			{"nonvideo", []store.MagnetFile{{Name: "readme.txt"}}, "", -1, store.StoreCodeTorBox, "torrent contains no allowed video files"},
			{"explicit extension", []store.MagnetFile{{Name: "movie.mkv"}}, "setup.exe", -1, store.StoreCodeTorBox, "requested file has a disallowed extension"},
			{"explicit blocked path", []store.MagnetFile{{Name: "movie.mkv", Path: "/setup.exe"}, {Name: "other.mp4"}}, "movie.mkv", -1, store.StoreCodeTorBox, "requested file has a disallowed extension"},
			{"explicit RD index", []store.MagnetFile{{Name: "movie.mkv", Idx: 0}, {Name: "setup.exe", Idx: 1}}, "", 1, store.StoreCodeRealDebrid, "requested file has a disallowed extension"},
			{"mixed", []store.MagnetFile{{Name: "movie.mkv"}, {Name: "setup.exe"}}, "", -1, store.StoreCodeTorBox, ""},
			{"index ignored outside RD", []store.MagnetFile{{Name: "movie.mkv", Idx: 0}, {Name: "setup.exe", Idx: 1}}, "", 1, store.StoreCodeTorBox, ""},
		} {
			t.Run(fmt.Sprintf("%s/allow=%t", tc.name, allow), func(t *testing.T) {
				reason := tc.reason
				if !allow && (tc.name == "mixed" || tc.name == "index ignored outside RD") {
					reason = "torrent contains disallowed files"
				}
				original := slices.Clone(tc.files)
				files, err := ValidatePlaybackFiles(tc.files, PlaybackFileRequest{
					Name:       tc.fileName,
					Index:      tc.fileIdx,
					StoreCode:  tc.code,
					AllowMixed: allow,
				})
				require.Equal(t, original, tc.files, "validation must not filter or rewrite evidence")
				if reason != "" {
					require.EqualError(t, err, reason)
					require.Empty(t, files)
				} else {
					require.NoError(t, err)
					require.Len(t, files, 1)
					require.Equal(t, "movie.mkv", files[0].GetName())
				}
			})
		}
	}
}

func TestValidatePlaybackFilesExcludesConfiguredVideoExtensions(t *testing.T) {
	previous := util.FileExtDisallowed
	t.Cleanup(func() { util.FileExtDisallowed = previous })
	var err error
	util.FileExtDisallowed, err = util.ParseDisallowedExtensions("+mkv")
	require.NoError(t, err)
	files := []store.MagnetFile{{Name: "movie.mkv"}, {Name: "movie.mp4"}}
	allowed, err := ValidatePlaybackFiles(files, PlaybackFileRequest{
		Index:      -1,
		StoreCode:  store.StoreCodeTorBox,
		AllowMixed: true,
	})
	require.NoError(t, err)
	require.Len(t, allowed, 1)
	require.Equal(t, "movie.mp4", allowed[0].GetName())
	_, err = ValidatePlaybackFiles(files, PlaybackFileRequest{
		Name:       "movie.mkv",
		Index:      -1,
		StoreCode:  store.StoreCodeTorBox,
		AllowMixed: true,
	})
	require.EqualError(t, err, "requested file has a disallowed extension")
}

func TestStoredEvidencePrecedence(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		files                torrent_stream.Files
		checked, mixed, want bool
	}{
		{"missing stays compatible", nil, false, false, true},
		{"nonvideo rejects", torrent_stream.Files{{Name: "readme.txt"}}, false, false, false},
		{"checked video clears nonvideo", torrent_stream.Files{{Name: "readme.txt"}}, true, false, true},
		{"checked video cannot clear strict blocked", torrent_stream.Files{{Name: "setup.exe"}}, true, false, false},
		{"checked video permits mixed", torrent_stream.Files{{Name: "setup.exe"}}, true, true, true},
		{"mixed still needs video", torrent_stream.Files{{Name: "setup.exe"}}, false, true, false},
		{"conflicting blocked path rejects", torrent_stream.Files{{Name: "movie.mkv", Path: "/setup.exe"}}, true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) { require.Equal(t, tc.want, StoredFilesAreViable(tc.files, tc.checked, tc.mixed)) })
	}
}
