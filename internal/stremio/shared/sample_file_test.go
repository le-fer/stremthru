package stremio_shared

import (
	"regexp"
	"testing"

	"github.com/MunifTanjim/stremthru/store"
	"github.com/stretchr/testify/require"
)

func TestMatchersExcludeSampleFiles(t *testing.T) {
	sample := &store.MagnetFile{Name: "example.series.s02e03.1080p.web.h264-group-sample.mkv", Path: "/Example.Series.S02E03.1080p.WEB.H264-GROUP/Sample/example.series.s02e03.1080p.web.h264-group-sample.mkv", Idx: 1, Size: 9999}
	full := &store.MagnetFile{Name: "example.series.s02e03.1080p.web.h264-group.mkv", Path: "/Example.Series.S02E03.1080p.WEB.H264-GROUP/example.series.s02e03.1080p.web.h264-group.mkv", Idx: 2, Size: 1000}
	files := []store.File{sample, full}
	require.Same(t, full, MatchFileByStremId("Example.Series.S02E03", files, "tt9990001:2:3", "", store.StoreCodeTorBox))
	require.Same(t, full, MatchFileByLargestSize(files), "even a larger sample must not win a movie fallback")
	require.Same(t, full, MatchFileByPattern(files, regexp.MustCompile("(?i)s02e03")))
	require.Nil(t, MatchFileByName(files, sample.Name))
	require.Nil(t, MatchFileByIdx(files, sample.Idx, store.StoreCodeRealDebrid))
	require.Same(t, full, MatchFileByName(files, full.Name))
	require.Same(t, full, MatchFileByIdx(files, full.Idx, store.StoreCodeRealDebrid))
	require.Nil(t, MatchFileByLargestSize([]store.File{sample}))
	require.Nil(t, MatchFileByStremId("Example.Series.S02E03", []store.File{sample}, "tt9990001:2:3", "", store.StoreCodeTorBox))
	require.Len(t, files, 2, "selection must preserve supplied evidence")
}

func TestExcludeSampleFilesPreservesProviderEvidence(t *testing.T) {
	sample := &store.MagnetFile{Name: "Example.Series.S02E03.mkv", Path: "/Sample/Example.Series.S02E03.mkv", Idx: 0, Size: 9000}
	first := &store.MagnetFile{Name: "Example.Series.S02E03.mkv", Path: "/Example.Series.S02E03.mkv", Idx: 7, Size: 1000}
	second := &store.MagnetFile{Name: "Example.Series.S02E04.mkv", Path: "/Example.Series.S02E04.mkv", Idx: 2, Size: 2000}
	files := []store.File{sample, first, second}
	filtered := ExcludeSampleFiles(files)
	require.Len(t, filtered, 2)
	require.Same(t, first, filtered[0])
	require.Same(t, second, filtered[1])
	require.Same(t, sample, files[0], "filtering must not overwrite evidence in the backing array")
	filtered[0] = second
	require.Same(t, first, files[1], "the presentation slice must not alias the evidence slice")
	require.Empty(t, ExcludeSampleFiles([]store.File{sample}))
}
