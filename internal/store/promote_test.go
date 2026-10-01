package store

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
)

func TestPromoteCopiesGraySnapshotIntoEffectiveVersion(t *testing.T) {
	st, err := Open(t.TempDir() + "/store.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	publishVersion(t, st, "payments", "prod", "", mustItems(t, "a", `"old"`, "n", "1"))
	publishVersion(t, st, "payments", "prod", "canary",
		mustItems(t, "a", `"new"`, "n", "1.0", "flag", "true", "empty", "null"))

	promoted, err := st.Promote(ctx, "payments", "prod", 2)
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	if promoted.Version != 3 {
		t.Fatalf("version = %d, want 3", promoted.Version)
	}
	if promoted.EffectiveGrayTag() != "" {
		t.Fatalf("promoted version must not carry a gray tag: %q", promoted.EffectiveGrayTag())
	}
	if promoted.PromotionSource() != 2 {
		t.Fatalf("promotionOf = %d, want 2", promoted.PromotionSource())
	}
	if promoted.RollbackSource() != 0 {
		t.Fatalf("promotion rollbackOf = %d, want 0", promoted.RollbackSource())
	}

	effective, err := st.EffectiveVersion(ctx, "payments", "prod")
	if err != nil {
		t.Fatalf("effective: %v", err)
	}
	if effective != 3 {
		t.Fatalf("effective = %d, want 3 after promotion", effective)
	}

	items, err := st.Items(ctx, "payments", "prod", 3)
	if err != nil {
		t.Fatalf("items: %v", err)
	}
	want := map[string]string{"a": `"new"`, "n": "1.0", "flag": "true", "empty": "null"}
	if len(items) != len(want) {
		t.Fatalf("items = %v, want %v", items, want)
	}
	for name, value := range want {
		if string(items[name]) != value {
			t.Fatalf("item %s = %s, want %s", name, items[name], value)
		}
	}

	versions, err := st.ListVersions(ctx, "payments", "prod")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(versions) != 3 {
		t.Fatalf("history length = %d, want 3", len(versions))
	}
	source := versions[1]
	if !source.GrayTag.Valid || source.GrayTag.String != "canary" || source.PromotionOf.Valid {
		t.Fatalf("source version must stay untouched: %+v", source)
	}
	if versions[0].PromotionOf.Valid || versions[0].RollbackOf.Valid {
		t.Fatalf("normal release must not carry promotionOf/rollbackOf")
	}
	target := versions[2]
	if target.GrayTag.Valid || !target.PromotionOf.Valid || target.PromotionOf.Int64 != 2 || target.RollbackOf.Valid {
		t.Fatalf("promoted metadata = %+v", target)
	}
}

func TestPromoteRejectsNonGrayAndMissingVersionsWithoutSideEffects(t *testing.T) {
	st, err := Open(t.TempDir() + "/store.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	publishVersion(t, st, "payments", "prod", "", mustItems(t, "a", `"1"`))
	publishVersion(t, st, "payments", "prod", "canary", mustItems(t, "a", `"2"`))
	publishVersion(t, st, "payments", "prod", "", mustItems(t, "a", `"3"`))

	if _, err := st.Promote(ctx, "payments", "prod", 1); !errors.Is(err, ErrNotGray) {
		t.Fatalf("promote full release err = %v, want ErrNotGray", err)
	}
	if _, err := st.Promote(ctx, "payments", "prod", 3); !errors.Is(err, ErrNotGray) {
		t.Fatalf("promote later full release err = %v, want ErrNotGray", err)
	}
	if _, err := st.Promote(ctx, "payments", "prod", 99); !errors.Is(err, ErrNotFound) {
		t.Fatalf("promote missing err = %v, want ErrNotFound", err)
	}
	if _, err := st.Promote(ctx, "other", "env", 2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("promote other scope err = %v, want ErrNotFound", err)
	}

	versions, err := st.ListVersions(ctx, "payments", "prod")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(versions) != 3 {
		t.Fatalf("failed promotions must not create versions, got %d", len(versions))
	}
	for _, v := range versions {
		if v.PromotionOf.Valid {
			t.Fatalf("failed promotions must not leave promotionOf: %+v", v)
		}
	}
	effective, err := st.EffectiveVersion(ctx, "payments", "prod")
	if err != nil {
		t.Fatalf("effective: %v", err)
	}
	if effective != 3 {
		t.Fatalf("effective = %d, must stay 3", effective)
	}
}

func TestPromoteSerializesConcurrentWritesWithoutGapsOrDuplicates(t *testing.T) {
	st, err := Open(t.TempDir() + "/store.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	publishVersion(t, st, "payments", "prod", "", mustItems(t, "a", `"1"`))
	publishVersion(t, st, "payments", "prod", "canary", mustItems(t, "a", `"2"`))

	const writers = 8
	var wg sync.WaitGroup
	wg.Add(writers)
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		go func() {
			defer wg.Done()
			switch i % 3 {
			case 0:
				_, err := st.Promote(ctx, "payments", "prod", 2)
				errs <- err
			case 1:
				_, err := st.Publish(ctx, PublishInput{
					Namespace: "payments", Environment: "prod",
					Items: map[string]json.RawMessage{"a": json.RawMessage(`"x"`)},
				})
				errs <- err
			default:
				_, err := st.Rollback(ctx, "payments", "prod", 1)
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent write: %v", err)
		}
	}

	versions, err := st.ListVersions(ctx, "payments", "prod")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(versions) != 2+writers {
		t.Fatalf("version count = %d, want %d", len(versions), 2+writers)
	}
	for i, v := range versions {
		if v.Version != int64(i+1) {
			t.Fatalf("version sequence gap/duplicate at %d: %d", i, v.Version)
		}
	}
}
