package stremio_shared

import (
	"fmt"
	"strings"
	"testing"

	"github.com/MunifTanjim/stremthru/store"
	"github.com/stretchr/testify/require"
)

func TestFilePathMatchesSeason(t *testing.T) {
	tests := []struct {
		folder string
		season int
		want   bool
	}{
		{"16x9", 13, false},
		{"4x3", 13, false},
		{"1x02", 13, false},
		{"05-13", 13, false},
		{"2024", 13, false},
		{"Synthetic Series 2024", 13, false},
		{"Complete 13 Seasons", 5, false},
		{"S13", 13, true},
		{"S05", 13, false},
		{"s005", 5, true},
		{"Season-05", 13, false},
		{"Season_005", 5, true},
		{"Season 005", 0, false},
		{"Season 100", 100, true},
		{"Season 100", 10, false},
		{"Season 000", 0, true},
		{"Seasons 001-013", 13, true},
		{"Seasons 001-013", 14, false},
		{"S01-S13", 13, true},
		{"S01-S13", 14, false},
		{"Seasons 1 to 13", 13, true},
		{"Seasons 1 thru 13", 13, true},
		{"Season 1 - Season 13", 13, true},
		{"Seasons 1 2 3", 2, true},
		{"Seasons 1, 2, 3", 3, true},
		{"S01+S13", 13, true},
		{"S01.S13", 13, true},
		{"Synthetic.S05.1080p", 13, false},
		{"Synthetic.S05.2024", 13, false},
		{"Synthetic.16x9.S05", 13, false},
		{"Synthetic.S05E10", 13, false},
		{"Saison 05", 13, false},
		{"Seizoen 05", 13, false},
		{"Sezon 05", 13, false},
		{"Stagione 05", 13, false},
		{"Temporada 05", 13, false},
		{"Сезон 05", 13, false},
		{"5th Season", 13, false},
		{"5 сезон", 13, false},
		{"5ª temporada", 13, false},
		{"XS05", 13, false},
		{"S05Edition", 13, false},
		{"Season 999999999999999999999999999999", 13, false},

		{"Sezonul 5", 13, false},
		{"SEZONUL 005", 5, true},
		{"Sezonul-13", 13, true},
		{"Sezonul 005", 0, false},
		{"Staffel 5", 13, false},
		{"5. Staffel", 13, false},
		{"Staffeln 1-13", 13, true},
		{"Сезон №5", 13, false},
		{"Сезон №005", 5, true},
		{"Säsong 5", 13, false},
		{"Sasong 5", 13, false},
		{"Sesong 5", 13, false},
		{"Sæson 5", 13, false},
		{"Sason 5", 13, false},
		{"Kausi 5", 13, false},
		{"Sezóna 5", 13, false},
		{"Évad 5", 13, false},
		{"5. évad", 13, false},
		{"第5季", 13, false},
		{"第5期", 13, false},
		{"剧名第5季全集", 13, false},
		{"シーズン5", 13, false},
		{"作品シーズン5", 13, false},
		{"시즌5", 13, false},
		{"작품시즌5", 13, false},
		{"第1-13季", 13, true},
		{"Complete 5 Staffeln", 13, false},
		{"Seasons 1, 13", 5, false},
		{"S01+S13", 5, false},
		{"Seasons 1 & 13", 5, false},
		{"Seasons 1-3, 5-7", 4, false},
		{"Seasons 1-3, 5-7", 6, true},
		{"Season 1:13", 13, true},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s season %d", tt.folder, tt.season), func(t *testing.T) {
			require.Equal(t, tt.want, FilePathMatchesSeason("/"+tt.folder+"/E10.mkv", tt.season))
		})
	}
}

func TestResolveFileSeason(t *testing.T) {
	tests := []struct {
		name           string
		filename       []int
		path           string
		fallback, want int
	}{
		{"filename wins", []int{13}, "/Season 5/E10.mkv", 5, 13},
		{"filename wins over conflicting folders", []int{13}, "/Season 5/Season 2/E10.mkv", 5, 13},
		{"folder wins over title", nil, "/Season 5/E10.mkv", 13, 5},
		{"no evidence uses title", nil, "/Synthetic/E10.mkv", 13, 13},
		{"aspect ratio uses title", nil, "/16x9/E10.mkv", 13, 13},
		{"matching pack uses title", nil, "/S01-S13/E10.mkv", 13, 13},
		{"ambiguous pack stays unknown", nil, "/S01-S13/E10.mkv", -1, -1},
		{"title outside pack stays unknown", nil, "/S01-S13/E10.mkv", 14, -1},
		{"specific folder narrows pack", nil, "/S01-S13/Season 5/E10.mkv", 13, 5},
		{"contradictory folders stay unknown", nil, "/Season 5/Season 13/E10.mkv", 13, -1},
		{"two singleton labels use matching fallback", nil, "/S01.S13/E10.mkv", 13, 13},
		{"two singleton labels stay unknown", nil, "/S01.S13/E10.mkv", -1, -1},
		{"later range contains fallback", nil, "/S01-S05.S13/E10.mkv", 13, 13},
		{"Romanian", nil, "/Sezonul 005/E10.mkv", 13, 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { require.Equal(t, tt.want, ResolveFileSeason(tt.filename, tt.path, tt.fallback)) })
	}
}

func TestNestedSeasonPacksRemainBounded(t *testing.T) {
	// On the old implementation depth 4 already yields 16 duplicate intervals;
	// fail there before trying deeper inputs that would exhaust memory.
	for _, depth := range []int{4, 8, 16, 64} {
		filePath := strings.Repeat("/S01-S13", depth) + "/E10.mkv"
		spans := filePathSeasons(filePath)
		require.Len(t, spans, 1, "depth %d must retain a canonical union", depth)
		require.Equal(t, fileSeasonRange{1, 13}, spans[0])
		require.True(t, FilePathMatchesSeason(filePath, 13))
		require.False(t, FilePathMatchesSeason(filePath, 14))
	}
}

func TestSeasonPackCountsAndCJKRanges(t *testing.T) {
	for _, folder := range []string{
		"Synthetic.Complete.13.Seasons", "Synthetic.Complete.5.Staffeln",
		"Synthetic.Complete.5.Saisons", "Synthetic.Complete.5.Stagioni",
	} {
		t.Run(folder, func(t *testing.T) {
			filePath := "/" + folder + "/E10.mkv"
			require.False(t, FilePathMatchesSeason(filePath, 3), "a season count supplies no positive evidence")
			require.Equal(t, -1, ResolveFileSeason(nil, filePath, -1))
			require.Equal(t, 3, ResolveFileSeason(nil, filePath, 3))
		})
	}
	for _, folder := range []string{"第1季-第13季", "第1期-第13期", "作品第1季〜第13季", "第1季至第13季"} {
		t.Run(folder, func(t *testing.T) {
			filePath := "/" + folder + "/E10.mkv"
			require.True(t, FilePathMatchesSeason(filePath, 3))
			require.False(t, FilePathMatchesSeason(filePath, 14))
			require.Equal(t, 3, ResolveFileSeason(nil, filePath, 3))
		})
	}
}

func TestSeasonMarkersDoNotMistakeTitleInitialsForSeasons(t *testing.T) {
	for _, folder := range []string{"S.Y.N.T.2024", "Synthetic S.2024", "Synthetic T 2024"} {
		filePath := "/" + folder + "/Season 13/E10.mkv"
		require.True(t, FilePathMatchesSeason(filePath, 13), folder)
		require.False(t, FilePathMatchesSeason(filePath, 5), folder)
		require.Equal(t, 13, ResolveFileSeason(nil, filePath, -1), folder)
	}
	for _, folder := range []string{"S 05", "T.05", "S_05", "S-05", "Synthetic.S05", "Synthetic.T05", "Season 100"} {
		require.False(t, FilePathMatchesSeason("/"+folder+"/E10.mkv", 13), folder)
	}
}

func TestSeasonListsIgnorePunctuationInsideLabels(t *testing.T) {
	for _, folder := range []string{
		"Seasons 1 and Season-13", "Seasons 1, Season-13", "Seasons 1 & Season:13",
		"S01+S-13", "Seasons 1, Sezonul-13", "Seasons 1, Staffel:13",
	} {
		t.Run(folder, func(t *testing.T) {
			filePath := "/Synthetic/" + folder + "/E10.mkv"
			for season := 0; season <= 14; season++ {
				require.Equal(t, season == 1 || season == 13, FilePathMatchesSeason(filePath, season), "season %d", season)
			}
			require.Equal(t, -1, ResolveFileSeason(nil, filePath, 5))
			require.Equal(t, 13, ResolveFileSeason(nil, filePath, 13))
			require.Nil(t, matchFileByIMDBStremId([]store.File{&store.MagnetFile{Name: "E10.mkv", Path: filePath}}, "tt9990001:5:10"))
		})
	}
	for _, folder := range []string{"Season 1-Season-13", "Seasons 1 to Season-13", "Seasons 1 thru Season:13", "第1季-第13季", "第1季至第13季"} {
		t.Run(folder, func(t *testing.T) {
			filePath := "/Synthetic/" + folder + "/E10.mkv"
			for season := 0; season <= 14; season++ {
				require.Equal(t, season >= 1 && season <= 13, FilePathMatchesSeason(filePath, season), "season %d", season)
			}
		})
	}
}

func TestSeasonWordsInTitlesAreNotFolderEvidence(t *testing.T) {
	for _, label := range []string{
		"Season", "Seasons", "Saison", "Saisons", "Seizoen", "Seizoenen",
		"Sezon", "Sezonul", "Sezony", "Stagione", "Stagioni", "Temporada", "Temporadas",
		"Staffel", "Staffeln", "Säsong", "Säsonger", "Sesong", "Sesonger", "Sæson", "Sæsoner",
		"Kausi", "Kaudet", "Évad", "Évadok", "Sezóna", "Sezóny", "Сезон", "Сезоны", "シーズン", "시즌",
	} {
		for _, suffix := range []string{"", " Something", " (2024)", " 2024", ".2024.1080p", " 2"} {
			title := "Synthetic " + label + suffix
			t.Run(title, func(t *testing.T) {
				context := NewFileSeasonContext(title)
				root := "/" + title
				require.False(t, FilePathMatchesSeason(root+"/E02.mkv", 1, context), "title text is not positive season evidence")
				require.Equal(t, -1, ResolveFileSeason(nil, root+"/E02.mkv", -1, context))
				require.True(t, FilePathMatchesSeason(root+"/Season 1/E02.mkv", 1, context))
				require.False(t, FilePathMatchesSeason(root+"/Season 5/E02.mkv", 1, context))
				require.True(t, context.MatchesSeason(title+".S01E02.mkv", root+"/Season 5/video.mkv", 1))
			})
		}
	}
}

func TestStrictSeasonEvidence(t *testing.T) {
	tests := []struct {
		path   string
		season int
		want   bool
	}{
		{"/Synthetic Series/E10.mkv", 3, false},
		{"/Synthetic Series Season 2/E10.mkv", 3, false},
		{"/Synthetic Series Season 2/E10.mkv", 2, true},
		{"/Synthetic Series Season 2 1080p/E10.mkv", 3, false},
		{"/Synthetic Series Season 2 [1080p]/E10.mkv", 2, true},
		{"/Synthetic Series Season 2 WEB-DL H.264 DDP5.1/E10.mkv", 2, true},
		{"/Season 2 5.1/E10.mkv", 2, true},
		{"/Season 2 5.1/E10.mkv", 5, false},
		{"/Synthetic Series Season 2 5.1/E10.mkv", 2, true},
		{"/Synthetic Series Season 2 5.1/E10.mkv", 5, false},
		{"/Synthetic.Series.Season.2.1080p.AMZN.WEB-DL.DDP5.1.H.264-SyntheticGroup/E10.mkv", 2, true},
		{"/Synthetic Four Seasons 2/E10.mkv", 2, false},
		{"/S03/Synthetic Series Season 2/E10.mkv", 3, false},
		{"/Synthetic Series Season 2/Season 3/E10.mkv", 3, false},
		{"/S01-S03/Synthetic Series Season 2/E10.mkv", 3, false},
		{"/S01-S03/Synthetic Series Season 2/E10.mkv", 2, true},
		{"/Synthetic Series Season 1 & Season 2/E10.mkv", 1, true},
		{"/Synthetic Series Season 1 & Season 2/E10.mkv", 2, true},
		{"/Synthetic Series Season 1 & Season 2/E10.mkv", 3, false},
		{"/Synthetic Series Seasons 1-3/E10.mkv", 3, true},
		{"/Synthetic Last Season 2024/E10.mkv", 3, false},
		{"/Synthetic Last Season 2 Something/E10.mkv", 2, false},
		{"/Synthetic.Season.2024-S01/E10.mkv", 1, true},
		{"/Season 1.Season 5/E10.mkv", 5, true},
		{"/Season 1.Season 5/E10.mkv", 2, false},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s season %d", tt.path, tt.season), func(t *testing.T) { require.Equal(t, tt.want, FilePathMatchesSeason(tt.path, tt.season)) })
	}
	for _, label := range []string{"Season", "Saison", "Seizoen", "Sezon", "Sezonul", "Stagione", "Temporada", "Staffel", "Säsong", "Sesong", "Sæson", "Kausi", "Évad", "Sezóna", "Сезон", "シーズン", "시즌"} {
		root := "/Synthetic Series " + label + " 2"
		require.False(t, FilePathMatchesSeason(root+"/E10.mkv", 3), label)
		require.True(t, FilePathMatchesSeason(root+"/E10.mkv", 2), label)
		require.False(t, FilePathMatchesSeason("/S03"+root+"/E10.mkv", 3), label)
	}
}

func TestVerifiedTitlesAndTorrentFallback(t *testing.T) {
	context := NewFileSeasonContext("Synthetic Series Season 2", "Synthetic Series")
	require.True(t, FilePathMatchesSeason("/S03/SYNTHETIC.SERIES.SEASON.2/E10.mkv", 3, context))
	require.False(t, FilePathMatchesSeason("/Synthetic Series Season 2/E10.mkv", 3, context))
	require.False(t, FilePathMatchesSeason("/Season 2/E10.mkv", 3, context), "title protection is not a global label exclusion")
	require.False(t, FilePathMatchesSeason("/Synthetic Series Season 20/E10.mkv", 3, context), "partial titles must not be masked")
	require.True(t, context.MatchesSeason("Synthetic.Series.Season.2.S03E10.mkv", "/Season 5/file.mkv", 3))
	require.False(t, context.MatchesSeason("Synthetic.Series.Season.2.E10.mkv", "/Season 5/file.mkv", 3))
	for _, title := range []string{"Synthetic.Series.S03.1080p", "Synthetic Series Season 3 [1080p]"} {
		c := context.WithTorrentTitle(title)
		require.True(t, FilePathMatchesSeason("/Synthetic Series/E10.mkv", 3, c), title)
		require.False(t, FilePathMatchesSeason("/Season 2/E10.mkv", 3, c), title)
		require.False(t, FilePathMatchesSeason("/S02/Season 3/E10.mkv", 3, c), title)
	}
	for _, title := range []string{"Synthetic Series Complete", "Synthetic Series S01-S03", "Synthetic Series 2024", "Synthetic Series Season 2"} {
		require.False(t, FilePathMatchesSeason("/Synthetic Series/E10.mkv", 3, context.WithTorrentTitle(title)), title)
	}
}
