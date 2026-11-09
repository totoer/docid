package docid

import (
	"encoding/base64"
	"errors"
	"io"
	"strings"

	"github.com/cockroachdb/pebble"
)

// readView couples document reads with the matching collection/index state.
// Its helpers never acquire locks: callers must protect both for the full read.
type readView struct {
	reader interface {
		Get([]byte) ([]byte, io.Closer, error)
		NewIter(*pebble.IterOptions) (*pebble.Iterator, error)
	}
	collections map[string]*collectionState
}

// One collection snapshot owns both index kinds. Transaction mutations detach
// the state, its maps and trees together before publishing a new snapshot.
type collectionState struct {
	indexes     map[string]*index
	textIndexes map[string]*textIndex
}

func newCollectionState() *collectionState {
	return &collectionState{indexes: make(map[string]*index), textIndexes: make(map[string]*textIndex)}
}

func (s *collectionState) hasIndexes() bool { return len(s.indexes) != 0 || len(s.textIndexes) != 0 }

// Internal operations only poll cancellation; they do not need Deadline/Done
// or a goroutine to combine a query's context with its transaction's context.
type operationContext interface {
	Err() error
}

func (db *DB) readView() readView {
	return readView{reader: db.store, collections: db.collections}
}

func (v readView) walk(prefix string, visit func(key string, raw []byte) error) error {
	// Every prefix used here ends in '/', whose successor is '0'.
	upper := prefix[:len(prefix)-1] + "0"
	it, err := v.reader.NewIter(&pebble.IterOptions{LowerBound: []byte(prefix), UpperBound: []byte(upper)})
	if err != nil {
		return err
	}
	defer it.Close()
	for it.First(); it.Valid(); it.Next() {
		if err := visit(string(it.Key()), it.Value()); err != nil {
			return err
		}
	}
	return it.Error()
}

func (v readView) scan(collection string, visit func(string, Document) error) error {
	return v.scanRecords(collection, recordMode{document: true}, func(item record) error {
		return visit(item.id, item.doc)
	})
}

func (v readView) scanRecords(collection string, mode recordMode, visit func(record) error) error {
	prefix := docPrefix(collection)
	return v.walk(prefix, func(key string, raw []byte) error {
		id, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(key, prefix))
		if err != nil {
			return err
		}
		item, err := readRecord(string(id), raw, mode)
		if err != nil {
			return err
		}
		return visit(item)
	})
}

func (v readView) getRecord(collection, id string, mode recordMode) (record, error) {
	raw, closer, err := v.reader.Get(docKey(collection, id))
	if errors.Is(err, pebble.ErrNotFound) {
		return record{}, ErrNotFound
	}
	if err != nil {
		return record{}, err
	}
	defer closer.Close()
	return readRecord(id, raw, mode)
}

func readRecord(id string, raw []byte, mode recordMode) (record, error) {
	item := record{id: id}
	var err error
	if mode.document {
		item.doc, err = decodeDocument(raw)
	} else {
		err = validateDocument(raw)
	}
	if err != nil {
		return record{}, err
	}
	if mode.raw {
		// Pebble values expire when their iterator advances or closer closes.
		// Typed results own their bytes beyond iterator/closer and lock lifetimes.
		item.raw = string(raw)
	}
	return item, nil
}

func (v readView) get(collection, id string) (Document, error) {
	return v.readDocument(collection, id, true)
}

// Writes without indexes only need to check existence, not allocate a document
// tree. Still validate stored JSON so corruption keeps the same error behavior.
func (v readView) readDocument(collection, id string, materialize bool) (Document, error) {
	raw, closer, err := v.reader.Get(docKey(collection, id))
	if errors.Is(err, pebble.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	if !materialize {
		return nil, validateDocument(raw)
	}
	return decodeDocument(raw)
}
