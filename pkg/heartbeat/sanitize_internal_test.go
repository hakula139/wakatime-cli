package heartbeat

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHideProjectFolder(t *testing.T) {
	tests := map[string]struct {
		ProjectPath         string
		ProjectPathOverride string
		Entity              string
		Expected            string
		RootCountRemoved    bool
	}{
		"auto-detected": {
			ProjectPath:      "/usr/temp",
			Entity:           "/usr/temp/project/main.go",
			Expected:         "project/main.go",
			RootCountRemoved: true,
		},
		"override": {
			ProjectPath:         "/original/folder",
			ProjectPathOverride: "/usr/temp",
			Entity:              "/usr/temp/project/main.go",
			Expected:            "project/main.go",
			RootCountRemoved:    true,
		},
		"windows path": {
			ProjectPath:      `C:/Users/wakatime/programming/study/learn_flutter`,
			Entity:           `C:/Users/wakatime/programming/study/learn_flutter/lib/main.dart`,
			Expected:         `lib/main.dart`,
			RootCountRemoved: true,
		},
		"outside both folders": {
			ProjectPath:         "/home/u/src",
			ProjectPathOverride: "/home/u/other",
			Entity:              "/home/u/src-notes/drafts/main.go",
			Expected:            "main.go",
			RootCountRemoved:    true,
		},
		"windows network path outside the project": {
			ProjectPath:      `C:/Users/user/project`,
			Entity:           `\\fileserver\shared\workspace\main.go`,
			Expected:         "main.go",
			RootCountRemoved: true,
		},
		"already relative": {
			ProjectPath: "/home/u/src",
			Entity:      "project/main.go",
			Expected:    "project/main.go",
		},
		"remote": {
			ProjectPath: "/home/u/src",
			Entity:      "ssh://example.com/workspace/main.go",
			Expected:    "ssh://example.com/workspace/main.go",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			h := hideProjectFolder(Heartbeat{
				Entity:              test.Entity,
				ProjectPath:         test.ProjectPath,
				ProjectPathOverride: test.ProjectPathOverride,
				ProjectRootCount:    PointerTo(4),
			}, true)

			assert.Equal(t, test.Expected, h.Entity)
			if test.RootCountRemoved {
				assert.Nil(t, h.ProjectRootCount)
			} else {
				assert.Equal(t, PointerTo(4), h.ProjectRootCount)
			}
		})
	}
}
