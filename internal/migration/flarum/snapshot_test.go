package flarum

import (
	"strings"
	"testing"
)

func TestSnapshotCountsAndCanonicalDigest(t *testing.T) {
	base := `{"format_version":1,"source":"flarum","generated_at":"2026-01-01T00:00:00Z","tables":{"users":{"source_count":0,"rows":[]},"groups":{"source_count":0,"rows":[]},"group_user":{"source_count":0,"rows":[]},"group_permission":{"source_count":0,"rows":[]},"discussions":{"source_count":0,"rows":[]},"posts":{"source_count":0,"rows":[]},"tags":{"source_count":0,"rows":[]},"discussion_tag":{"source_count":0,"rows":[]},"post_likes":{"source_count":0,"rows":[]},"flags":{"source_count":0,"rows":[]},"notifications":{"source_count":0,"rows":[]}}}`
	first, digest1, err := ReadSnapshot(strings.NewReader(base))
	if err != nil {
		t.Fatal(err)
	}
	secondText := strings.Replace(base, "2026-01-01", "2026-01-02", 1)
	second, digest2, err := ReadSnapshot(strings.NewReader(secondText))
	if err != nil {
		t.Fatal(err)
	}
	if digest1 != digest2 {
		t.Fatal("generated_at unexpectedly changes snapshot content digest")
	}
	if err := first.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := second.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotRejectsCountMismatchAndUnknownSource(t *testing.T) {
	valid := `{"format_version":1,"source":"flarum","tables":{"users":{"source_count":1,"rows":[]},"groups":{"source_count":0,"rows":[]},"group_user":{"source_count":0,"rows":[]},"group_permission":{"source_count":0,"rows":[]},"discussions":{"source_count":0,"rows":[]},"posts":{"source_count":0,"rows":[]},"tags":{"source_count":0,"rows":[]},"discussion_tag":{"source_count":0,"rows":[]},"post_likes":{"source_count":0,"rows":[]},"flags":{"source_count":0,"rows":[]},"notifications":{"source_count":0,"rows":[]}}}`
	if _, _, err := ReadSnapshot(strings.NewReader(valid)); err == nil {
		t.Fatal("accepted a mismatched source count")
	}
	unknown := strings.Replace(valid, `"source":"flarum"`, `"source":"other"`, 1)
	if _, _, err := ReadSnapshot(strings.NewReader(unknown)); err == nil {
		t.Fatal("accepted an unsupported source")
	}
}
