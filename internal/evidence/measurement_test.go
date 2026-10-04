package evidence

import (
	"encoding/json"
	"testing"
	"time"
)

func TestChildTTFTAbsentUntilObserved(t *testing.T) {
	for _, observed := range []bool{false, true} {
		child := ChildResult{Outcome: OutcomeFailure}
		if observed {
			now := time.Now()
			child.FirstTokenAt = &now
		}
		data, err := json.Marshal(child)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]any
		if err := json.Unmarshal(data, &fields); err != nil {
			t.Fatal(err)
		}
		_, present := fields["ttft_seconds"]
		if present != observed {
			t.Fatalf("observation=%v JSON=%s", observed, data)
		}
	}
}
