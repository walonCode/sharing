package protocol

import (
	"encoding/json"
	"testing"
)

func FuzzMessageDecode(f *testing.F) {
	f.Add([]byte(`{"type":"clip","mime":"text/plain","data":"hi","ts":1}`))
	f.Add([]byte(`{"type":"offer","id":"x","size":9007199254740993}`))
	f.Add([]byte(`[]`))
	f.Fuzz(func(t *testing.T, data []byte) {
		var m Message
		if err := json.Unmarshal(data, &m); err != nil {
			return
		}
		// A decoded message must round-trip without panicking.
		if _, err := json.Marshal(m); err != nil {
			t.Fatal(err)
		}
	})
}

func TestUnknownFieldsAndTypesDecode(t *testing.T) {
	var m Message
	if err := json.Unmarshal([]byte(`{"type":"from-the-future","extra":{"a":1}}`), &m); err != nil || m.Type != "from-the-future" {
		t.Fatalf("%+v %v", m, err)
	}
}
