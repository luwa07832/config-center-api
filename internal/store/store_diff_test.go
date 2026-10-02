package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func mustItems(t *testing.T, pairs ...string) map[string]json.RawMessage {
	t.Helper()
	if len(pairs)%2 != 0 {
		t.Fatalf("pairs must alternate name/value")
	}
	items := map[string]json.RawMessage{}
	for i := 0; i < len(pairs); i += 2 {
		items[pairs[i]] = json.RawMessage(pairs[i+1])
	}
	return items
}

func publishVersion(t *testing.T, st *Store, ns, env, grayTag string, items map[string]json.RawMessage) Version {
	t.Helper()
	v, err := st.Publish(context.Background(), PublishInput{Namespace: ns, Environment: env, GrayTag: grayTag, Items: items})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	return v
}

func TestPublishIncrementsVersionsAndKeepsGrayIneffective(t *testing.T) {
	st, err := Open(t.TempDir() + "/store.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	v1 := publishVersion(t, st, "payments", "prod", "", mustItems(t, "a", `"1"`, "b", "true"))
	v2 := publishVersion(t, st, "payments", "prod", "canary", mustItems(t, "a", `"2"`))
	v3 := publishVersion(t, st, "orders", "prod", "", mustItems(t, "a", `"x"`))

	if v1.Version != 1 || v2.Version != 2 || v3.Version != 1 {
		t.Fatalf("versions = %d,%d,%d, want 1,2,1", v1.Version, v2.Version, v3.Version)
	}
	effective, err := st.EffectiveVersion(ctx, "payments", "prod")
	if err != nil {
		t.Fatalf("effective: %v", err)
	}
	if effective != 1 {
		t.Fatalf("effective = %d, want 1 (gray v2 must not become effective)", effective)
	}

	loaded, err := st.Items(ctx, "payments", "prod", 2)
	if err != nil {
		t.Fatalf("items: %v", err)
	}
	if string(loaded["a"]) != `"2"` {
		t.Fatalf("gray value = %s", loaded["a"])
	}
	if tag := v2.EffectiveGrayTag(); tag != "canary" {
		t.Fatalf("gray tag = %q", tag)
	}
	if v2.RollbackSource() != 0 {
		t.Fatalf("new release must not have a rollback source")
	}
}

func TestRollbackCopiesSnapshotAndRecordsSource(t *testing.T) {
	st, err := Open(t.TempDir() + "/store.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	publishVersion(t, st, "payments", "prod", "", mustItems(t, "a", `"1"`))
	publishVersion(t, st, "payments", "prod", "", mustItems(t, "a", `"2"`))
	rb, err := st.Rollback(ctx, "payments", "prod", 1)
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if rb.Version != 3 || rb.RollbackSource() != 1 {
		t.Fatalf("rollback = version %d source %d", rb.Version, rb.RollbackSource())
	}
	items, err := st.Items(ctx, "payments", "prod", 3)
	if err != nil {
		t.Fatalf("items: %v", err)
	}
	if string(items["a"]) != `"1"` {
		t.Fatalf("rolled back value = %s, want \"1\"", items["a"])
	}

	if _, err := st.Rollback(ctx, "payments", "prod", 99); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing source err = %v, want ErrNotFound", err)
	}
}

func TestGetVersionDistinguishesMissingAndOtherScope(t *testing.T) {
	st, err := Open(t.TempDir() + "/store.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	publishVersion(t, st, "payments", "prod", "", mustItems(t, "a", `"1"`))
	publishVersion(t, st, "payments", "prod", "", mustItems(t, "a", `"1b"`))
	publishVersion(t, st, "orders", "stage", "", mustItems(t, "a", `"2"`))

	if _, err := st.GetVersion(ctx, "orders", "stage", 2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross scope get err = %v, want ErrNotFound", err)
	}
	found, err := st.FindVersion(ctx, 2)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if found.Namespace != "payments" || found.Environment != "prod" {
		t.Fatalf("found scope = %s/%s", found.Namespace, found.Environment)
	}
	if _, err := st.FindVersion(ctx, 99); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing find err = %v, want ErrNotFound", err)
	}
}

func TestCanonicalJSONPreservesValueSemantics(t *testing.T) {
	cases := []string{`1`, `1.5`, `true`, `false`, `null`, `"text"`, `[]`, `{"b":1,"a":2}`}
	for _, raw := range cases {
		canonical, err := CanonicalJSON(json.RawMessage(raw))
		if err != nil {
			t.Fatalf("canonical %s: %v", raw, err)
		}
		if string(canonical) == "" {
			t.Fatalf("empty canonical for %s", raw)
		}
	}
	a, _ := CanonicalJSON(json.RawMessage(`{"b": 1, "a": 2}`))
	b, _ := CanonicalJSON(json.RawMessage(`{"a":2,"b":1}`))
	if string(a) != string(b) {
		t.Fatalf("object key order must canonicalize: %s vs %s", a, b)
	}
	n1, _ := CanonicalJSON(json.RawMessage(`1`))
	n2, _ := CanonicalJSON(json.RawMessage(`1.0`))
	if string(n1) == string(n2) {
		t.Fatalf("1 and 1.0 must keep distinct number semantics")
	}
	if _, err := CanonicalJSON(json.RawMessage(`not-json`)); err == nil {
		t.Fatalf("invalid JSON must be rejected")
	}
}
