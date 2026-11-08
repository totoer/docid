package docid

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/cockroachdb/pebble"
)

var (
	ErrClosed             = errors.New("docid: database closed")
	ErrNotFound           = errors.New("docid: document not found")
	ErrCollectionNotFound = errors.New("docid: collection not found")
	ErrAlreadyExists      = errors.New("docid: already exists")
	ErrUniqueConstraint   = errors.New("docid: unique index violation")
	ErrInvalidDocument    = errors.New("docid: expected a JSON object")
	ErrInvalidQuery       = errors.New("docid: invalid query")
	ErrInvalidName        = errors.New("docid: invalid name")
	ErrInvalidDestination = errors.New("docid: invalid decode destination")
)

// DB is safe for concurrent use. A query observes one consistent database state.
type DB struct {
	mu          sync.RWMutex
	store       *pebble.DB
	collections map[string]map[string]*index
	closed      bool
}

// Open opens or creates a database directory and rebuilds its persisted indexes.
func Open(path string) (*DB, error) {
	store, err := pebble.Open(path, &pebble.Options{})
	if err != nil {
		return nil, err
	}
	db := &DB{store: store, collections: make(map[string]map[string]*index)}
	if err := db.load(); err != nil {
		_ = store.Close()
		return nil, err
	}
	return db, nil
}

// Close waits for in-flight operations. Repeated Close calls succeed.
func (db *DB) Close() error {
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.closed {
		return nil
	}
	db.closed = true
	return db.store.Close()
}

func validName(name string) bool {
	return name != "" && strings.TrimSpace(name) == name && utf8.ValidString(name)
}
func enc(value string) string             { return base64.RawURLEncoding.EncodeToString([]byte(value)) }
func docPrefix(collection string) string  { return "d/" + enc(collection) + "/" }
func docKey(collection, id string) []byte { return []byte(docPrefix(collection) + enc(id)) }
func indexKey(collection, name string) []byte {
	return []byte("i/" + enc(collection) + "/" + enc(name))
}

// Collection creates a named collection if needed and returns its handle.
func (db *DB) Collection(name string) (*Collection, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.closed {
		return nil, ErrClosed
	}
	if !validName(name) {
		return nil, ErrInvalidName
	}
	if _, ok := db.collections[name]; !ok {
		if err := db.store.Set([]byte("c/"+enc(name)), []byte("{}"), pebble.Sync); err != nil {
			return nil, err
		}
		db.collections[name] = make(map[string]*index)
	}
	return &Collection{db: db, name: name}, nil
}

// Collections returns collection names in lexical order.
func (db *DB) Collections() ([]string, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()
	if db.closed {
		return nil, ErrClosed
	}
	names := make([]string, 0, len(db.collections))
	for name := range db.collections {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

func (db *DB) walk(prefix string, visit func(key string, raw []byte) error) error {
	// Every prefix used here ends in '/', whose successor is '0'.
	upper := prefix[:len(prefix)-1] + "0"
	it, err := db.store.NewIter(&pebble.IterOptions{LowerBound: []byte(prefix), UpperBound: []byte(upper)})
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

func (db *DB) load() error {
	if err := db.walk("c/", func(key string, _ []byte) error {
		name, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(key, "c/"))
		if err != nil || !validName(string(name)) {
			return fmt.Errorf("docid: corrupt collection key %q", key)
		}
		db.collections[string(name)] = make(map[string]*index)
		return nil
	}); err != nil {
		return err
	}
	if err := db.walk("i/", func(key string, raw []byte) error {
		parts := strings.Split(key, "/")
		if len(parts) != 3 {
			return fmt.Errorf("docid: corrupt index key %q", key)
		}
		name, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			return err
		}
		indexes, ok := db.collections[string(name)]
		if !ok {
			return ErrCollectionNotFound
		}
		var def Index
		if err := json.Unmarshal(raw, &def); err != nil {
			return err
		}
		if !validName(def.Name) || !validPath(def.Field) || enc(def.Name) != parts[2] {
			return fmt.Errorf("docid: corrupt index definition")
		}
		indexes[def.Name] = newIndex(def)
		return nil
	}); err != nil {
		return err
	}
	for name, indexes := range db.collections {
		if len(indexes) == 0 {
			continue
		}
		if err := db.scan(name, func(id string, doc Document) error {
			for _, idx := range indexes {
				if err := idx.check(id, doc); err != nil {
					return err
				}
				idx.add(id, doc)
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

func (db *DB) scan(collection string, visit func(string, Document) error) error {
	return db.scanRecords(collection, recordMode{document: true}, func(item record) error {
		return visit(item.id, item.doc)
	})
}

func (db *DB) scanRecords(collection string, mode recordMode, visit func(record) error) error {
	prefix := docPrefix(collection)
	return db.walk(prefix, func(key string, raw []byte) error {
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

func (db *DB) getRecord(collection, id string, mode recordMode) (record, error) {
	raw, closer, err := db.store.Get(docKey(collection, id))
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
		// Typed results own the bytes they use after releasing the DB lock.
		item.raw = string(raw)
	}
	return item, nil
}

func (db *DB) get(collection, id string) (Document, error) {
	return db.readDocument(collection, id, true)
}

// Writes without indexes only need to check existence, not allocate a document
// tree. Still validate stored JSON so corruption keeps the same error behavior.
func (db *DB) readDocument(collection, id string, materialize bool) (Document, error) {
	raw, closer, err := db.store.Get(docKey(collection, id))
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
