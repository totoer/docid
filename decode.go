package docid

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
)

func decodeDestination(dst any, slice bool) (reflect.Value, error) {
	v := reflect.ValueOf(dst)
	if !v.IsValid() || v.Kind() != reflect.Pointer || v.IsNil() {
		return reflect.Value{}, fmt.Errorf("%w: expected a non-nil pointer", ErrInvalidDestination)
	}
	v = v.Elem()
	if slice && v.Kind() != reflect.Slice {
		return reflect.Value{}, fmt.Errorf("%w: expected a pointer to a slice", ErrInvalidDestination)
	}
	return v, nil
}

// Decode into a fresh value so neither existing maps nor pointer fields can be
// modified on failure. Invoke user unmarshallers only after releasing DB locks.
func decodeInto(ctx context.Context, raw []byte, dst reflect.Value) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	value := reflect.New(dst.Type())
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(value.Interface()); err != nil {
		return fmt.Errorf("docid: decode: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	dst.Set(value.Elem())
	return nil
}

// Assemble only the array/alias envelope around stored JSON. In particular, do
// not re-encode Document maps or build an intermediate []Document for projection.
// The full array is still passed through encoding/json in one call so custom
// slice unmarshallers and atomic destination replacement keep their semantics.
func (r *queryResult) json(ctx context.Context, selected int) ([]byte, error) {
	type aliasKey struct {
		position int
		json     []byte
	}
	var aliases []aliasKey
	if selected < 0 {
		aliases = make([]aliasKey, len(r.aliases))
		for i, alias := range r.aliases {
			key, _ := json.Marshal(alias)
			aliases[i] = aliasKey{position: i, json: key}
		}
		sort.Slice(aliases, func(i, j int) bool { return r.aliases[aliases[i].position] < r.aliases[aliases[j].position] })
	}
	size := 2 + max(0, len(r.rows)-1)
	recordSize := func(item *record) int {
		if item == nil {
			return 4 // null
		}
		return len(item.raw)
	}
	for _, row := range r.rows {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if selected >= 0 {
			size += recordSize(row[selected])
		} else {
			size += 2 + max(0, len(aliases)-1)
			for _, alias := range aliases {
				size += len(alias.json) + 1 + recordSize(row[alias.position])
			}
		}
	}
	raw := make([]byte, 0, size)
	raw = append(raw, '[')
	appendRecord := func(item *record) {
		if item == nil {
			raw = append(raw, "null"...)
		} else {
			raw = append(raw, item.raw...)
		}
	}
	for i, row := range r.rows {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if i > 0 {
			raw = append(raw, ',')
		}
		if selected >= 0 {
			appendRecord(row[selected])
		} else {
			raw = append(raw, '{')
			for j, alias := range aliases {
				if j > 0 {
					raw = append(raw, ',')
				}
				raw = append(raw, alias.json...)
				raw = append(raw, ':')
				appendRecord(row[alias.position])
			}
			raw = append(raw, '}')
		}
	}
	return append(raw, ']'), nil
}
