package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestItemValuesReturnsStoredValuesInVersionOrder(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "service.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	for _, input := range []PublishInput{
		{Namespace: "svc", Environment: "dev", Items: map[string]json.RawMessage{"x": json.RawMessage(`1`)}},
		{Namespace: "svc", Environment: "dev", Items: map[string]json.RawMessage{"y": json.RawMessage(`2`)}},
		{Namespace: "svc", Environment: "dev", GrayTag: "gray", Items: map[string]json.RawMessage{"x": json.RawMessage(`3`)}},
		{Namespace: "other", Environment: "dev", Items: map[string]json.RawMessage{"x": json.RawMessage(`9`)}},
	} {
		if _, err := st.Publish(ctx, input); err != nil {
			t.Fatalf("publish: %v", err)
		}
	}

	values, err := st.ItemValues(ctx, "svc", "dev", "x")
	if err != nil {
		t.Fatalf("item values: %v", err)
	}
	if len(values) != 2 {
		t.Fatalf("values length = %d, want 2", len(values))
	}
	if values[0].Version != 1 || string(values[0].Value) != `1` {
		t.Fatalf("values[0] = %+v", values[0])
	}
	if values[1].Version != 3 || string(values[1].Value) != `3` {
		t.Fatalf("values[1] = %+v", values[1])
	}

	missing, err := st.ItemValues(ctx, "svc", "dev", "missing")
	if err != nil {
		t.Fatalf("missing item values: %v", err)
	}
	if len(missing) != 0 {
		t.Fatalf("missing values = %v, want empty", missing)
	}
}
