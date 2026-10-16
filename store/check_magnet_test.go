package store

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCheckMagnetDataJSONPreservesRecheckFailureWithoutDetails(t *testing.T) {
	item := CheckMagnetDataItem{Hash: "0123456789012345678901234567890123456789", Status: MagnetStatusCached}
	data := CheckMagnetData{
		Items:        []CheckMagnetDataItem{item},
		RecheckError: errors.New("private provider error"),
	}

	encoded, err := json.Marshal(data)
	require.NoError(t, err)
	require.JSONEq(t, `{"items":[{"hash":"0123456789012345678901234567890123456789","magnet":"","status":"cached","files":null}],"recheck_failed":true}`, string(encoded))
	require.NotContains(t, string(encoded), "private provider error")

	var decoded CheckMagnetData
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	require.Equal(t, []CheckMagnetDataItem{item}, decoded.Items)
	require.ErrorIs(t, decoded.RecheckError, ErrCheckMagnetRecheckFailed)
}

func TestCheckMagnetDataJSONDefaultsToNoRecheckFailure(t *testing.T) {
	encoded, err := json.Marshal(CheckMagnetData{})
	require.NoError(t, err)
	require.JSONEq(t, `{"items":null,"recheck_failed":false}`, string(encoded))

	var decoded CheckMagnetData
	require.NoError(t, json.Unmarshal([]byte(`{"items":[]}`), &decoded))
	require.Empty(t, decoded.Items)
	require.NoError(t, decoded.RecheckError)
}
