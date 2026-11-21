package heartbeat

import (
	"cmp"
	"encoding/json"
	"math"
	"sort"
	"time"
)

// AIBufferDuration is the fixed UTC window for buffering AI activity.
const AIBufferDuration = 5 * time.Minute

// CompactAIHeartbeats preserves prompt events and combines zero-prompt AI activity
// with matching wire metadata within fixed UTC windows and uninterrupted runs of
// the same project. It retains both activity endpoints and places each group's
// metric totals on its last heartbeat.
func CompactAIHeartbeats(heartbeats []Heartbeat) []Heartbeat {
	ordered := append([]Heartbeat(nil), heartbeats...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].Time < ordered[j].Time
	})

	type groupKey struct {
		bucket   float64
		identity string
	}

	type heartbeatGroup struct {
		first int
		last  int
	}

	groups := make(map[groupKey]*heartbeatGroup)
	seen := make(map[string]struct{})
	retained := make([]bool, len(ordered))

	var previousProject string

	for i, h := range ordered {
		retained[i] = true

		bufferedAI := h.Category == aiCodingCategoryString && h.AISession != "" && !h.SkipAIBuffering
		if bufferedAI {
			id := h.ID()
			if _, ok := seen[id]; ok {
				retained[i] = false
				continue
			}

			seen[id] = struct{}{}
		}

		var project string
		if h.Project != nil {
			project = *h.Project
		}

		if project != previousProject {
			clear(groups)

			previousProject = project
		}

		if !bufferedAI || h.AIPromptLength != 0 {
			continue
		}

		identity := h
		identity.Time = 0
		clearAICounts(&identity)
		identity.AILineChanges = lineChangeSign(h.AILineChanges)
		identity.HumanLineChanges = lineChangeSign(h.HumanLineChanges)
		wireBytes, _ := json.Marshal(identity)
		key := groupKey{bucket: math.Floor(h.Time / AIBufferDuration.Seconds()), identity: string(wireBytes)}

		group, ok := groups[key]
		if !ok {
			groups[key] = &heartbeatGroup{first: i, last: i}
			continue
		}

		if h.Time == ordered[group.first].Time {
			mergeAICounts(&ordered[group.first], h)

			retained[i] = false

			continue
		}

		mergeAICounts(&ordered[i], ordered[group.last])

		if group.last == group.first {
			clearAICounts(&ordered[group.first])
		} else {
			retained[group.last] = false
		}

		group.last = i
	}

	compacted := make([]Heartbeat, 0, len(ordered))

	for i, h := range ordered {
		if retained[i] {
			compacted = append(compacted, h)
		}
	}

	return compacted
}

func lineChangeSign(changes *int) *int {
	if changes == nil {
		return nil
	}

	sign := cmp.Compare(*changes, 0)

	return &sign
}

func clearAICounts(h *Heartbeat) {
	h.AILineChanges = nil
	h.HumanLineChanges = nil
	h.AIPromptLength = 0
	h.AIInputTokens = 0
	h.AICachedInputTokens = 0
	h.AIOutputTokens = 0
}

func mergeAICounts(dst *Heartbeat, src Heartbeat) {
	dst.AILineChanges = addIntPointers(dst.AILineChanges, src.AILineChanges)
	dst.HumanLineChanges = addIntPointers(dst.HumanLineChanges, src.HumanLineChanges)
	dst.AIPromptLength += src.AIPromptLength
	dst.AIInputTokens += src.AIInputTokens
	dst.AICachedInputTokens += src.AICachedInputTokens
	dst.AIOutputTokens += src.AIOutputTokens
}

func addIntPointers(dst, src *int) *int {
	switch {
	case dst == nil:
		return src
	case src == nil:
		return dst
	default:
		sum := *dst + *src

		return &sum
	}
}
