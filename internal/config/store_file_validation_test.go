package config

import "testing"

func TestParseStoreRecheckMissingFileLists(t *testing.T) {
	for _, tc := range []struct {
		value   string
		enabled bool
	}{
		{"true", true},
		{"false", false},
		{" true ", true},
		{"1", true},
		{"0", false},
	} {
		t.Run(tc.value, func(t *testing.T) {
			enabled, err := parseFileValidationBool("STREMTHRU_STORE_RECHECK_MISSING_FILE_LISTS", tc.value)
			if err != nil || enabled != tc.enabled {
				t.Fatalf("enabled=%v err=%v; want %v", enabled, err, tc.enabled)
			}
		})
	}
	if _, err := parseFileValidationBool("STREMTHRU_STORE_RECHECK_MISSING_FILE_LISTS", "invalid"); err == nil {
		t.Fatal("accepted invalid boolean")
	}
}

func TestLoadStoreFileListRecheckDefaultsAndConfiguredValues(t *testing.T) {
	const sameProvider = "STREMTHRU_STORE_RECHECK_MISSING_FILE_LISTS"
	const crossStore = "STREMTHRU_STORE_RECHECK_MISSING_FILE_LISTS_CROSS_STORE"
	for _, tc := range []struct {
		name      string
		same      string
		cross     string
		wantSame  bool
		wantCross bool
	}{
		{"empty uses enabled defaults", "", "", true, true},
		{"disable same provider only", "false", "true", false, true},
		{"disable cross store only", "true", "false", true, false},
		{"disable both", "false", "false", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(sameProvider, tc.same)
			t.Setenv(crossStore, tc.cross)
			if got := loadFileValidationBool(sameProvider); got != tc.wantSame {
				t.Fatalf("same-provider recheck=%v; want %v", got, tc.wantSame)
			}
			if got := loadFileValidationBool(crossStore); got != tc.wantCross {
				t.Fatalf("cross-store recheck=%v; want %v", got, tc.wantCross)
			}
		})
	}
	if _, err := parseFileValidationBool(crossStore, "invalid"); err == nil {
		t.Fatal("accepted invalid cross-store recheck setting")
	}
}

func TestFileValidationCentralDefaults(t *testing.T) {
	for name, expected := range map[string]bool{
		"STREMTHRU_STORE_RECHECK_MISSING_FILE_LISTS":             true,
		"STREMTHRU_STORE_RECHECK_MISSING_FILE_LISTS_CROSS_STORE": true,
		"STREMTHRU_ALLOW_VIDEO_TORRENTS_WITH_DISALLOWED_FILES":   false,
	} {
		t.Run(name, func(t *testing.T) {
			if _, found := defaultValueByEnv[""][name]; !found {
				t.Fatal("missing central default")
			}
			for _, value := range []string{"", "   ", "true", "false"} {
				t.Setenv(name, value)
				want := expected
				if value == "true" {
					want = true
				}
				if value == "false" {
					want = false
				}
				if got := loadFileValidationBool(name); got != want {
					t.Fatalf("value=%q got=%v want=%v", value, got, want)
				}
			}
			for _, value := range []string{"", "invalid"} {
				if _, err := parseFileValidationBool(name, value); err == nil {
					t.Fatalf("accepted invalid resolved value %q", value)
				}
			}
		})
	}
}
