package jara

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type chatCrunchLimitWindow struct {
	UsedPercent        float64 `json:"usedPercent"`
	WindowDurationMins int64   `json:"windowDurationMins"`
	ResetsAt           int64   `json:"resetsAt"`
}

type chatCrunchLimitBucket struct {
	Primary              *chatCrunchLimitWindow `json:"primary"`
	Secondary            *chatCrunchLimitWindow `json:"secondary"`
	RateLimitReachedType json.RawMessage        `json:"rateLimitReachedType"`
}

func chatCrunchIsUsageLimit(info json.RawMessage, message string) bool {
	lower := strings.ToLower(string(info) + " " + message)
	compact := strings.NewReplacer("_", "", "-", "", " ", "").Replace(lower)
	usageLimitMessage := strings.Contains(lower, "usage limit") && (strings.Contains(lower, "exceed") || strings.Contains(lower, "reach") || strings.Contains(lower, "hit"))
	return strings.Contains(compact, "usagelimitexceeded") || usageLimitMessage
}

func chatCrunchRateLimitState(result json.RawMessage, now time.Time) (bool, time.Time, error) {
	var response struct {
		RateLimits          *chatCrunchLimitBucket           `json:"rateLimits"`
		RateLimitsByLimitID map[string]chatCrunchLimitBucket `json:"rateLimitsByLimitId"`
	}
	if err := json.Unmarshal(result, &response); err != nil {
		return false, time.Time{}, fmt.Errorf("decode rate limits: %w", err)
	}
	buckets := make([]chatCrunchLimitBucket, 0, len(response.RateLimitsByLimitID)+1)
	for _, bucket := range response.RateLimitsByLimitID {
		buckets = append(buckets, bucket)
	}
	if response.RateLimits != nil {
		buckets = append(buckets, *response.RateLimits)
	}
	if len(buckets) == 0 {
		return false, time.Time{}, fmt.Errorf("the agent returned no usage-limit windows")
	}
	blocked := false
	var nextReset time.Time
	for _, bucket := range buckets {
		windows := crunchRelevantWindows(bucket)
		bucketBlocked := !crunchRateLimitIsNull(bucket.RateLimitReachedType)
		for _, window := range windows {
			if window.UsedPercent >= 100 && window.ResetsAt > now.Unix() {
				bucketBlocked = true
			}
		}
		if !bucketBlocked {
			continue
		}
		blocked = true
		for _, window := range windows {
			if window.ResetsAt <= now.Unix() {
				continue
			}
			reset := time.Unix(window.ResetsAt, 0)
			if nextReset.IsZero() || reset.Before(nextReset) {
				nextReset = reset
			}
		}
	}
	if blocked && nextReset.IsZero() {
		nextReset = now.Add(15 * time.Minute)
	}
	return blocked, nextReset, nil
}

func crunchRelevantWindows(bucket chatCrunchLimitBucket) []*chatCrunchLimitWindow {
	var relevant []*chatCrunchLimitWindow
	limitType := ""
	if len(bucket.RateLimitReachedType) > 0 && string(bucket.RateLimitReachedType) != "null" {
		_ = json.Unmarshal(bucket.RateLimitReachedType, &limitType)
	}
	switch strings.ToLower(limitType) {
	case "primary":
		if bucket.Primary != nil {
			relevant = append(relevant, bucket.Primary)
		}
	case "secondary":
		if bucket.Secondary != nil {
			relevant = append(relevant, bucket.Secondary)
		}
	default:
		if bucket.Primary != nil {
			relevant = append(relevant, bucket.Primary)
		}
		if bucket.Secondary != nil {
			relevant = append(relevant, bucket.Secondary)
		}
	}
	return relevant
}

func crunchRateLimitIsNull(value json.RawMessage) bool {
	if len(value) == 0 || string(value) == "null" {
		return true
	}
	var text string
	if json.Unmarshal(value, &text) == nil {
		return strings.TrimSpace(text) == ""
	}
	return false
}
