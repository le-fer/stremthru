package config

import (
	"encoding/json"
	"testing"

	"github.com/MunifTanjim/stremthru/internal/util"
	"github.com/stretchr/testify/require"
)

func TestBuildConfigDisplayFileValidation(t *testing.T) {
	previousAllow := AllowVideoTorrentsWithDisallowedFiles
	t.Cleanup(func() { AllowVideoTorrentsWithDisallowedFiles = previousAllow })
	previousIP := IP
	IP = &IPResolver{machineIP: "192.0.2.1"}
	t.Cleanup(func() { IP = previousIP })
	previousRecheck, previousCross, previousExtensions := StoreRecheckMissingFileLists, StoreRecheckMissingFileListsCrossStore, util.FileExtDisallowed
	t.Cleanup(func() {
		StoreRecheckMissingFileLists, StoreRecheckMissingFileListsCrossStore, util.FileExtDisallowed = previousRecheck, previousCross, previousExtensions
	})
	util.FileExtDisallowed = util.NewSet[string]()
	util.FileExtDisallowed.Add(".sh")
	util.FileExtDisallowed.Add(".exe")
	for _, flags := range [][2]bool{{false, false}, {false, true}, {true, false}, {true, true}} {
		StoreRecheckMissingFileLists, StoreRecheckMissingFileListsCrossStore = flags[0], flags[1]
		AllowVideoTorrentsWithDisallowedFiles = flags[0]
		stores := BuildConfigDisplay(nil).Stores
		require.Equal(t, flags[0], stores.AllowVideoTorrentsWithDisallowedFiles)
		require.Equal(t, flags[0], stores.RecheckMissingFileLists)
		require.Equal(t, flags[0] && flags[1], stores.RecheckMissingFileListsCrossStore)
		require.Equal(t, []string{".exe", ".sh"}, stores.DisallowedExtensions)
		blob, err := json.Marshal(stores)
		require.NoError(t, err)
		var exported map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(blob, &exported))
		for _, field := range []string{"allow_video_torrents_with_disallowed_files", "recheck_missing_file_lists", "recheck_missing_file_lists_cross_store", "disallowed_extensions"} {
			require.Contains(t, exported, field)
		}
	}
}
