//go:build (!freebsd && !openbsd && !netbsd && !dragonfly) || (freebsd && (amd64 || arm64)) || (openbsd && (amd64 || arm64))

package ai

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wakatime/wakatime-cli/pkg/heartbeat"
)

func TestCursorParseProjectsHistoricalRows(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("WAKATIME_HOME", t.TempDir())

	dbPath := filepath.Join(home, "Library", "Application Support", "Cursor", "User", "globalStorage", "state.vscdb")
	require.NoError(t, os.MkdirAll(filepath.Dir(dbPath), 0o755))

	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	_, err = db.Exec(`CREATE TABLE cursorDiskKV (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB)`)
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TABLE ItemTable (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB)`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO ItemTable(key, value) VALUES(?, ?)`, "cursorAuth/stripeMembershipType", "pro")
	require.NoError(t, err)

	after := time.Date(2026, 10, 12, 10, 0, 30, 0, time.UTC)
	historicalAt := after.Add(-10 * time.Second)
	editAt := after.Add(-cursorCutoffBuffer)
	textAt := after.Add(-2 * time.Second)
	oldPath := filepath.FromSlash("/home/u/src/app/old.go")
	mainPath := filepath.FromSlash("/home/u/src/app/main.go")

	const (
		oldBeforeID    = "cursor.content.old-before"
		oldAfterID     = "cursor.content.old-after"
		recentBeforeID = "cursor.content.recent-before"
		recentAfterID  = "cursor.content.recent-after"
	)

	_, err = db.Exec(`INSERT INTO cursorDiskKV(key, value) VALUES(?, ?)`, oldBeforeID, strings.Repeat("b", 2048))
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO cursorDiskKV(key, value) VALUES(?, ?)`, oldAfterID, strings.Repeat("a", 2048))
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO cursorDiskKV(key, value) VALUES(?, ?)`, recentBeforeID, "one\ntwo")
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO cursorDiskKV(key, value) VALUES(?, ?)`, recentAfterID, "one\ntwo\nthree")
	require.NoError(t, err)

	historicalResult := fmt.Sprintf(
		`{"beforeContentId":%q,"afterContentId":%q,"payload":%q}`,
		oldBeforeID,
		oldAfterID,
		strings.Repeat("r", 2048),
	)
	historicalValue, err := json.Marshal(map[string]any{
		"_v":        3,
		"type":      2,
		"createdAt": historicalAt.Format(time.RFC3339),
		"modelName": "composer-2.5",
		"tokenCount": map[string]any{
			"input_tokens":  40,
			"output_tokens": 20,
		},
		"toolFormerData": map[string]any{
			"status": "completed",
			"name":   "edit_file_v2",
			"params": fmt.Sprintf(`{"relativeWorkspacePath":%q}`, oldPath),
			"result": historicalResult,
		},
	})
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO cursorDiskKV(key, value) VALUES(?, ?)`, "bubbleId:session:historical", historicalValue)
	require.NoError(t, err)

	recentResult := fmt.Sprintf(`{"beforeContentId":%q,"afterContentId":%q}`, recentBeforeID, recentAfterID)
	recentEditValue, err := json.Marshal(map[string]any{
		"_v":        3,
		"type":      2,
		"createdAt": editAt.Format(time.RFC3339),
		"tokenCount": map[string]any{
			"input_tokens":  47,
			"output_tokens": 25,
		},
		"toolFormerData": map[string]any{
			"status": "completed",
			"name":   "edit_file_v2",
			"params": fmt.Sprintf(`{"relativeWorkspacePath":%q}`, mainPath),
			"result": recentResult,
		},
	})
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO cursorDiskKV(key, value) VALUES(?, ?)`, "bubbleId:session:recent-edit", recentEditValue)
	require.NoError(t, err)

	prompt := "Please update main.go"
	recentTextValue, err := json.Marshal(map[string]any{
		"_v":        3,
		"type":      1,
		"createdAt": textAt.Format(time.RFC3339),
		"text":      prompt,
	})
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO cursorDiskKV(key, value) VALUES(?, ?)`, "bubbleId:session:recent-text", recentTextValue)
	require.NoError(t, err)

	budget := &aiSQLiteBudget{bytes: aiSQLiteByteLimit - 1024}
	ctx := context.WithValue(t.Context(), aiSQLiteBudgetKey{}, budget)
	got, err := (Cursor{
		After:             after,
		FallbackUserAgent: "Cursor/1.128.0",
	}).Parse(ctx)
	require.NoError(t, err)
	require.Len(t, got, 2)

	assert.LessOrEqual(t, budget.bytes, int64(aiSQLiteByteLimit))

	assert.Equal(t, mainPath, got[0].Entity)
	assert.Equal(t, "session", got[0].AISession)
	assert.Equal(t, heartbeat.FileType, got[0].EntityType)
	assert.Equal(t, heartbeat.AICodingCategory.String(), got[0].Category)
	assert.Equal(t, float64(editAt.Unix()), got[0].Time)
	require.NotNil(t, got[0].AILineChanges)
	assert.Equal(t, 1, *got[0].AILineChanges)
	assert.Equal(t, int64(7), got[0].AIInputTokens)
	assert.Equal(t, int64(5), got[0].AIOutputTokens)
	require.NotNil(t, got[0].IsWrite)
	assert.True(t, *got[0].IsWrite)
	assert.Equal(t, "composer/2.5 Cursor/1.128.0", got[0].UserAgent)

	assert.Equal(t, "Cursor session", got[1].Entity)
	assert.Equal(t, "session", got[1].AISession)
	assert.Equal(t, heartbeat.AppType, got[1].EntityType)
	assert.Equal(t, heartbeat.AICodingCategory.String(), got[1].Category)
	assert.Equal(t, float64(textAt.Unix()), got[1].Time)
	assert.Equal(t, filepath.FromSlash("/home/u/src/app"), got[1].ProjectPathOverride)
	assert.Equal(t, len([]rune(prompt)), got[1].AIPromptLength)
	assert.Zero(t, got[1].AIInputTokens)
	assert.Zero(t, got[1].AIOutputTokens)
	require.NotNil(t, got[1].IsWrite)
	assert.False(t, *got[1].IsWrite)
	assert.Equal(t, "composer/2.5 Cursor/1.128.0", got[1].UserAgent)
}
