package debrider

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMapLinkAvailabilityResults(t *testing.T) {
	first, second := strings.Repeat("a", 40), strings.Repeat("b", 40)
	for _, tc := range []struct {
		name    string
		results []CheckLinkAvailabilityDataItem
		wantErr bool
	}{
		{"positional", []CheckLinkAvailabilityDataItem{{Cached: true}, {}}, false},
		{"aligned mixed identities", []CheckLinkAvailabilityDataItem{{Hash: first, Cached: true}, {}}, false},
		{"reordered identities", []CheckLinkAvailabilityDataItem{{Hash: second, Cached: true}, {Hash: first, Cached: true}}, false},
		{"sparse identity", []CheckLinkAvailabilityDataItem{{Hash: second, Cached: true}}, false},
		{"sparse hashless", []CheckLinkAvailabilityDataItem{{Cached: true}}, true},
		{"empty", nil, true},
		{"oversized hashless", []CheckLinkAvailabilityDataItem{{}, {}, {}}, true},
		{"reordered mixed identities", []CheckLinkAvailabilityDataItem{{Hash: second, Cached: true}, {}}, true},
		{"malformed", []CheckLinkAvailabilityDataItem{{Hash: "bad", Cached: true}, {}}, true},
		{"unrequested", []CheckLinkAvailabilityDataItem{{Hash: strings.Repeat("c", 40), Cached: true}, {}}, true},
		{"conflicting statuses", []CheckLinkAvailabilityDataItem{{Hash: first, Cached: true}, {Hash: first}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mapped, err := mapLinkAvailabilityResults([]string{first, second}, tc.results)
			if tc.wantErr {
				require.Error(t, err)
				require.Nil(t, mapped, "invalid batches must not expose partially mapped evidence")
			} else {
				require.NoError(t, err)
				for hash, item := range mapped {
					require.Contains(t, []string{first, second}, hash)
					require.Equal(t, hash, item.Hash)
				}
			}
		})
	}
}

func TestMapLinkAvailabilityResultsPreservesDuplicateEvidence(t *testing.T) {
	hash := strings.Repeat("a", 40)
	files := make([]CheckLinkAvailabilityDataItemFile, 1, 4)
	files[0].Name = "movie.mkv"
	results := []CheckLinkAvailabilityDataItem{
		{Hash: hash, Cached: true, Files: files},
		{Hash: strings.ToUpper(hash), Cached: true, Files: []CheckLinkAvailabilityDataItemFile{{Name: "setup.exe"}}},
		{Hash: hash, Cached: true, Files: []CheckLinkAvailabilityDataItemFile{{Name: "subtitle.srt"}}},
	}
	mapped, err := mapLinkAvailabilityResults([]string{hash}, results)
	require.NoError(t, err)
	require.Len(t, mapped, 1)
	require.Equal(t, []CheckLinkAvailabilityDataItemFile{{Name: "movie.mkv"}, {Name: "setup.exe"}, {Name: "subtitle.srt"}}, mapped[hash].Files)
	mapped[hash].Files[0].Name = "changed.mkv"
	require.Equal(t, "movie.mkv", results[0].Files[0].Name)
	require.Equal(t, strings.ToUpper(hash), results[1].Hash)
}
