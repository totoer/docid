// Package docid provides an embedded, collection-oriented JSON document database.
// Pebble stores documents and index definitions durably. B-trees index scalar
// fields in memory and are rebuilt on Open. Queries use a Go DSL with joins.
package docid
