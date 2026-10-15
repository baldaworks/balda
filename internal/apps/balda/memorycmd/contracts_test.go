package memorycmd

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestRecordRoundTrip(t *testing.T) {
	t.Parallel()
	for _, data := range []string{
		`{"version":2,"updated_at":"2026-10-11T00:00:00Z","entries":[{"version":1,"created_at":"2026-10-10T00:00:00Z","fact":"first\nmultiline fact"},{"version":2,"created_at":"2026-10-11T00:00:00Z","fact":"second fact"}]}`,
		`{"version":3,"updated_at":"2026-10-11T00:00:00Z","entries":[]}`,
	} {
		var rec Record
		if err := json.Unmarshal([]byte(data), &rec); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		var got, want any
		if err := json.Unmarshal(encoded, &got); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(data), &want); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("record JSON = %s, want %s", encoded, data)
		}
	}
}
