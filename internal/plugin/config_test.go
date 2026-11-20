package plugin

import (
	"bytes"
	"testing"
)

func TestNormalizeConfig(t *testing.T) {
	for _, tc := range []struct {
		name, raw, want string
	}{
		{"empty", `{}`, ""},
		{"values", `{"enabled":true,"label":"hello","limit":12}`, `{"enabled":true,"label":"hello","limit":12}`},
		{"reordered", `{"z":1,"a":false}`, `{"a":false,"z":1}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeConfig([]byte(tc.raw))
			if err != nil || string(got) != tc.want {
				t.Fatalf("got %s error=%v", got, err)
			}
		})
	}
	for _, raw := range []string{
		`[]`, `{"Bad":true}`, `{"a":{}}`, `{"a":1.5}`, `{"a":1,"a":2}`, `{"a":null}`, `{"a":"` + string(bytes.Repeat([]byte("x"), 257)) + `"}`,
	} {
		if _, err := NormalizeConfig([]byte(raw)); err == nil {
			t.Errorf("invalid config accepted: %s", raw[:min(len(raw), 40)])
		}
	}
	if _, err := NormalizeConfig([]byte(`{"a":` + string(bytes.Repeat([]byte("1"), 4097)) + `}`)); err == nil {
		t.Fatal("oversized config accepted")
	}
}
