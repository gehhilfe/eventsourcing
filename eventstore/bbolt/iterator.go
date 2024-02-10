package bbolt

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/hallgren/eventsourcing/core"
	"go.etcd.io/bbolt"
)

type iterator struct {
	tx     *bbolt.Tx
	cursor *bbolt.Cursor
	value  []byte
}

// Close closes the iterator
func (i *iterator) Close() {
	i.tx.Rollback()
}

func (i *iterator) Next() bool {
	var value []byte
	_, value = i.cursor.Next()

	if value == nil {
		return false
	}
	i.value = value
	return true
}

// Next return the next event
func (i *iterator) Value() (core.Event, error) {
	bEvent := boltEvent{}
	err := json.Unmarshal(i.value, &bEvent)
	if err != nil {
		return core.Event{}, errors.New(fmt.Sprintf("could not deserialize event, %v", err))
	}

	event := core.Event{
		AggregateID:   bEvent.AggregateID,
		AggregateType: bEvent.AggregateType,
		Version:       core.Version(bEvent.Version),
		GlobalVersion: core.Version(bEvent.GlobalVersion),
		Timestamp:     bEvent.Timestamp,
		Metadata:      bEvent.Metadata,
		Data:          bEvent.Data,
		Reason:        bEvent.Reason,
	}
	return event, nil
}
