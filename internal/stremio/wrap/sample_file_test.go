package stremio_wrap

import (
	"strings"
	"testing"

	stremio_transformer "github.com/MunifTanjim/stremthru/internal/stremio/transformer"
	"github.com/MunifTanjim/stremthru/stremio"
	"github.com/stretchr/testify/require"
)

func TestSampleStreamsCannotHideFullFileSibling(t *testing.T) {
	hash := strings.Repeat("a", 40)
	sample := WrappedStream{Stream: &stremio.Stream{InfoHash: hash, BehaviorHints: &stremio.StreamBehaviorHints{Filename: "Show.S01E02-sample.mkv"}}, r: &stremio_transformer.StreamExtractorResult{Hash: hash}}
	full := WrappedStream{Stream: &stremio.Stream{InfoHash: hash, BehaviorHints: &stremio.StreamBehaviorHints{Filename: "Show.S01E02.mkv"}}, r: &stremio_transformer.StreamExtractorResult{Hash: hash}}
	result := dedupeStreams(filterStreams([]WrappedStream{sample, full}, nil))
	require.Len(t, result, 1)
	require.Equal(t, "Show.S01E02.mkv", result[0].BehaviorHints.Filename)
	require.Empty(t, filterStreams([]WrappedStream{sample}, nil))
	sample.BehaviorHints = nil
	sample.r.File.Name = "Show.S01E02-sample.mkv"
	require.Empty(t, filterStreams([]WrappedStream{sample}, nil), "parsed filenames must be checked even without behavior hints")
}
