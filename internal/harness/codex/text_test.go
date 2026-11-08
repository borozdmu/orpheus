package codex

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestProjectedJSONNUL(t *testing.T) {
	for _, tc := range []struct{ name, raw, want string }{
		{"nested", `{"x":["a\u0000b",{"k\u0000":"v\u0000"}],"n":9007199254740993}`, `{"x":["a�b",{"k�":"v�"}],"n":9007199254740993}`},
		{"literal escape", `{"x":"\\u0000"}`, `{"x":"\\u0000"}`},
		{"mixed escapes", `["\u0000","\\u0000","\\\u0000"]`, `["�","\\u0000","\\�"]`},
		{"null", `null`, `null`},
		{"ordinary", `{"x":"Привет\n\t","n":1e-20}`, `{"x":"Привет\n\t","n":1e-20}`},
		{"key collision", `{"\u0000":1,"�":2}`, `"{\"\\u0000\":1,\"�\":2}"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := projectedJSON(json.RawMessage(tc.raw))
			var a, b any
			if err := json.Unmarshal(got, &a); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(tc.want), &b); err != nil || !reflect.DeepEqual(a, b) {
				t.Fatalf("got %s, want %s: %v", got, tc.want, err)
			}
			if string(projectedJSON(got)) != string(got) {
				t.Fatal("projection is not idempotent")
			}
		})
	}
	got := string(projectedJSON(json.RawMessage(`["\u0000",9007199254740993,1e-20]`)))
	if got != `["�",9007199254740993,1e-20]` {
		t.Fatalf("numeric precision changed: %s", got)
	}
}

func TestNormalizeNULAcrossOutputSources(t *testing.T) {
	for _, source := range []string{"aggregated", "json", "cached", "truncated"} {
		t.Run(source, func(t *testing.T) {
			item := NativeItem{ID: "tool", Type: "commandExecution", Status: "completed", Command: json.RawMessage(`["printf","a\u0000b"]`)}
			outputs := map[string]Output{}
			switch source {
			case "aggregated":
				item.AggregatedOutput = new("a\x00b")
			case "json":
				item.Result = json.RawMessage(`{"x":"a\u0000b"}`)
			case "cached":
				outputs[item.ID] = Bounded(json.RawMessage(`"a\u0000b"`), 1024, nil, false)
			case "truncated":
				outputs[item.ID] = Bounded(json.RawMessage(`"\u0000abcdefgh\u0000"`), 4, nil, false)
			}
			thread := Thread{Turns: []NativeTurn{{ID: "turn", Status: "failed", Error: json.RawMessage(`{"message":"bad\u0000value"}`), Items: []NativeItem{item, {ID: "answer", Type: "agentMessage", Text: "answer\x00text"}}}}}
			before, _ := json.Marshal(thread)
			turn := Normalize(thread, nil, outputs, 1024, nil, nil).Turns[0]
			tool := turn.Items[0]
			if turn.ErrorMessage != "bad�value" || turn.Items[1].Text != "answer�text" || string(tool.Input) != `["printf","a�b"]` {
				t.Fatalf("unprojected text: %+v", turn)
			}
			var result Result
			if err := json.Unmarshal(tool.Result, &result); err != nil {
				t.Fatal(err)
			}
			if source == "truncated" && (result.Head == nil || *result.Head != "�a" || result.Tail == nil || *result.Tail != "h�" || tool.Completeness != "truncated") {
				t.Fatalf("truncated result: %+v", result)
			}
			if source == "json" && string(result.Value) != `{"x":"a�b"}` {
				t.Fatalf("JSON result: %s", result.Value)
			}
			if (source == "cached" || source == "aggregated") && (result.Text == nil || *result.Text != "a�b") {
				t.Fatalf("text result: %+v", result)
			}
			after, _ := json.Marshal(thread)
			if string(before) != string(after) {
				t.Fatal("native data was mutated")
			}
		})
	}
}
