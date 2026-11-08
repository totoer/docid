package docid

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble"
)

func TestUpdateAdmission(t *testing.T) {
	db := openTest(t)
	calls := 0
	var escaped, copied *Tx
	fn := func(tx *Tx) error {
		calls++
		escaped = tx
		copy := *tx
		copied = &copy
		return tx.check()
	}
	if err := db.Update(nil, fn); !errors.Is(err, ErrInvalidTransaction) {
		t.Fatal(err)
	}
	if err := db.Update(context.Background(), nil); !errors.Is(err, ErrInvalidTransaction) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := db.Update(ctx, fn); !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
	if err := db.Update(context.Background(), fn); err != nil || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
	for _, tx := range []*Tx{nil, {}, escaped, copied} {
		if err := tx.check(); !errors.Is(err, ErrTxClosed) {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.Update(context.Background(), fn); !errors.Is(err, ErrClosed) || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

// Signals admission without relying on scheduling delays or sleep durations.
type observedContext struct {
	context.Context
	once    sync.Once
	checked chan struct{}
}

func (ctx *observedContext) Err() error {
	// Capture the result before signaling: cancellation by the test must be
	// caught by Update's second check, after it acquires the blocked mutex.
	err := ctx.Context.Err()
	ctx.once.Do(func() { close(ctx.checked) })
	return err
}

func receiveTxResult(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("transaction operation did not finish")
		return nil
	}
}

func TestUpdateCancellationAndCleanup(t *testing.T) {
	for _, outcome := range []string{"error", "panic", "cancel", "error-and-cancel", "commit-error"} {
		t.Run(outcome, func(t *testing.T) {
			db := openTest(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			reject := errors.New("business error")
			marker := &struct{ message string }{"panic marker"}
			var escaped *Tx
			var got error
			func() {
				if outcome == "panic" {
					defer func() {
						if p := recover(); p != marker {
							t.Fatalf("panic=%v", p)
						}
					}()
				}
				got = db.update(ctx, func(tx *Tx) error {
					escaped = tx
					if _, err := tx.Collection("staged"); err != nil {
						return err
					}
					switch outcome {
					case "error":
						return reject
					case "panic":
						panic(marker)
					case "cancel", "error-and-cancel":
						cancel()
						if _, err := tx.Collections(); !errors.Is(err, context.Canceled) {
							t.Fatalf("active canceled tx: %v", err)
						}
						if outcome == "error-and-cancel" {
							return reject
						}
					}
					return nil
				}, func(*pebble.Batch) error { return reject })
			}()
			if outcome != "panic" {
				want := reject
				if outcome == "cancel" {
					want = context.Canceled
				}
				if !errors.Is(got, want) {
					t.Fatalf("want %v, got %v", want, got)
				}
			}
			if _, err := escaped.Collections(); !errors.Is(err, ErrTxClosed) {
				t.Fatal(err)
			}
			if names, err := db.Collections(); err != nil || len(names) != 0 {
				t.Fatalf("%v: %v", names, err)
			}
			if err := db.Update(context.Background(), func(tx *Tx) error { _, err := tx.Collection("usable"); return err }); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUpdateCancellationWhileWaiting(t *testing.T) {
	db := openTest(t)
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &observedContext{Context: base, checked: make(chan struct{})}
	db.mu.Lock()
	done := make(chan error, 1)
	go func() { done <- db.Update(ctx, func(*Tx) error { return errors.New("callback must not run") }) }()
	<-ctx.checked
	cancel()
	db.mu.Unlock()
	if err := receiveTxResult(t, done); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestUpdateCancellationAfterCommitStarts(t *testing.T) {
	db := openTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	commits := 0
	err := db.update(ctx, func(tx *Tx) error { _, err := tx.Collection("committed"); return err }, func(batch *pebble.Batch) error {
		commits++
		cancel() // After the last cancellation check, at the commit boundary.
		return batch.Commit(pebble.Sync)
	})
	if err != nil || commits != 1 {
		t.Fatalf("commits=%d err=%v", commits, err)
	}
	if names, err := db.Collections(); err != nil || !reflect.DeepEqual(names, []string{"committed"}) {
		t.Fatalf("%v: %v", names, err)
	}
}

func TestUpdateExclusiveAccess(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		for _, operation := range []string{"read", "write", "query", "transaction", "close"} {
			t.Run(fmt.Sprintf("%s/rollback=%t", operation, rollback), func(t *testing.T) {
				db := openTest(t)
				c := collection(t, db, "docs")
				insert(t, c, "seed", Document{"n": 1})
				entered, release := make(chan struct{}), make(chan struct{})
				var once sync.Once
				unblock := func() { once.Do(func() { close(release) }) }
				defer unblock()
				reject := errors.New("rollback")
				txDone := make(chan error, 1)
				go func() {
					txDone <- db.Update(context.Background(), func(tx *Tx) error {
						if _, err := tx.Collection("staged"); err != nil {
							return err
						}
						close(entered)
						<-release
						if rollback {
							return reject
						}
						return nil
					})
				}()
				select {
				case <-entered:
				case <-time.After(10 * time.Second):
					t.Fatal("callback did not start")
				}
				// Directly verify the lock instead of inferring it from a sleep.
				if db.mu.TryRLock() {
					db.mu.RUnlock()
					t.Fatal("transaction does not hold exclusive lock")
				}
				started, done := make(chan struct{}), make(chan error, 1)
				go func() {
					close(started)
					var err error
					switch operation {
					case "read":
						_, err = c.Get("seed")
					case "write":
						err = c.Put("outside", Document{"n": 2})
					case "query":
						_, err = c.Find().All(context.Background())
					case "transaction":
						err = db.Update(context.Background(), func(tx *Tx) error { _, err := tx.Collection("next"); return err })
					case "close":
						err = db.Close()
					}
					done <- err
				}()
				<-started
				select {
				case err := <-done:
					t.Fatalf("operation completed inside transaction: %v", err)
				default:
				}
				unblock()
				if err := receiveTxResult(t, txDone); (rollback && !errors.Is(err, reject)) || (!rollback && err != nil) {
					t.Fatal(err)
				}
				if err := receiveTxResult(t, done); err != nil {
					t.Fatal(err)
				}
				if operation == "close" {
					if _, err := db.Collections(); !errors.Is(err, ErrClosed) {
						t.Fatal(err)
					}
				} else {
					names, err := db.Collections()
					if err != nil || slices.Contains(names, "staged") == rollback {
						t.Fatalf("%v: %v", names, err)
					}
				}
			})
		}
	}
}

func reopenTestDB(t *testing.T, db *DB, path string) *DB {
	t.Helper()
	if db != nil {
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
	next, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := next.Close(); err != nil {
			t.Error(err)
		}
	})
	return next
}

func TestTxCollections(t *testing.T) {
	path := t.TempDir()
	db := reopenTestDB(t, nil, path)
	collection(t, db, "existing")
	var escaped *Tx
	if err := db.Update(context.Background(), func(tx *Tx) error {
		escaped = tx
		for _, name := range []string{"", " bad", "bad ", "\xff"} {
			if _, err := tx.Collection(name); !errors.Is(err, ErrInvalidName) {
				t.Fatalf("%q: %v", name, err)
			}
		}
		for _, name := range []string{"z/ü", "a", "existing", "a"} {
			c, err := tx.Collection(name)
			if err != nil || c.Name() != name {
				t.Fatalf("%q: %v", name, err)
			}
		}
		names, err := tx.Collections()
		if err != nil || !reflect.DeepEqual(names, []string{"a", "existing", "z/ü"}) {
			t.Fatalf("%v: %v", names, err)
		}
		names[0] = "detached"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := escaped.Collections(); !errors.Is(err, ErrTxClosed) {
		t.Fatal(err)
	}
	if _, err := escaped.Collection("later"); !errors.Is(err, ErrTxClosed) {
		t.Fatal(err)
	}
	db = reopenTestDB(t, db, path)
	want := []string{"a", "existing", "z/ü"}
	if names, err := db.Collections(); err != nil || !reflect.DeepEqual(names, want) {
		t.Fatalf("%v: %v", names, err)
	}
	reject := errors.New("reject collection")
	if err := db.Update(context.Background(), func(tx *Tx) error {
		if _, err := tx.Collection("rolled-back"); err != nil {
			return err
		}
		return reject
	}); !errors.Is(err, reject) {
		t.Fatal(err)
	}
	db = reopenTestDB(t, db, path)
	if names, err := db.Collections(); err != nil || !reflect.DeepEqual(names, want) {
		t.Fatalf("%v: %v", names, err)
	}
}
