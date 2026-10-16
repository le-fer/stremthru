package stremio_torz

import (
	"testing"

	stremio_testutil "github.com/MunifTanjim/stremthru/internal/stremio/testutil"
)

func TestPlaybackRejectsDisallowedFiles(t *testing.T) {
	stremio_testutil.CheckPlayback(t, handleStrem)
}
