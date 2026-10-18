package core

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsSampleFile(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		sample     bool
	}{
		{"example.series.s02e03.1080p.web.h264-group-sample.mkv", "", true},
		{"movie_SAMPLE.MKV", "", true},
		{"sample-movie.mkv", "", true},
		{"sample.mkv", "", true},
		{"E03.mkv", "/Show/Sample/E03.mkv", true},
		{"E03.mkv", "/Show/sAMPLES/E03.mkv", true},
		{"E03.mkv", `C:\Show\Sample\E03.mkv`, true},
		{"", "/Show/Sample/E03.mkv", true},
		{"E03.mkv", "/Show/E03-sample.mkv", true},
		{"example.series.s02e03.1080p.web.h264-group.mkv", "", false},
		{"The.Sample.Movie.2024.mkv", "", false},
		{"Sample.Title.2024.1080p.mkv", "", false},
		{"Sample Title.2024.1080p.mkv", "", false},
		{"Example.Series.S02E03.1080p.WEB-sample", "", true},
		{"Example.Series.S02E03.1080p.WEB.sample", "", true},
		{"Sampled.2024.mkv", "", false},
		{"Resample.mkv", "", false},
		{"E03.mkv", "/Show.Sample.2024/E03.mkv", false},
		{"", "", false},
	} {
		t.Run(tc.name+tc.path, func(t *testing.T) { require.Equal(t, tc.sample, IsSampleFile(tc.name, tc.path)) })
	}
	require.True(t, HasVideoExtension("movie-sample.mkv"), "sample detection must not redefine video extensions")
}
