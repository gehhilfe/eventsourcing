package bbolt

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/hallgren/eventsourcing/core"
	"go.etcd.io/bbolt"
)

const (
	globalEventOrderBucketName = "global_event_order"
)

// itob returns an 8-byte big endian representation of v.
func itob(v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return b
}

// BBolt is the eventstore handler
type BBolt struct {
	db *bbolt.DB // The bbolt db where we store everything
}

type boltEvent struct {
	AggregateID   string
	Version       uint64
	GlobalVersion uint64
	Reason        string
	AggregateType string
	Timestamp     time.Time
	Data          []byte
	Metadata      []byte // map[string]interface{}
}

// MustOpenBBolt opens the event stream found in the given file. If the file is not found it will be created and
// initialized. Will panic if it has problems persisting the changes to the filesystem.
func MustOpenBBolt(dbFile string) *BBolt {
	db, err := bbolt.Open(dbFile, 0600, &bbolt.Options{
		Timeout: 1 * time.Second,
	})
	if err != nil {
		panic(err)
	}

	// Ensure that we have a bucket to store the global event ordering
	err = db.Update(func(tx *bbolt.Tx) error {
		if _, err := tx.CreateBucketIfNotExists([]byte(globalEventOrderBucketName)); err != nil {
			return errors.New("could not create global event order bucket")
		}
		return nil
	})
	if err != nil {
		panic(err)
	}
	return &BBolt{
		db: db,
	}
}

// Save an aggregate (its events)
func (e *BBolt) Save(events []core.Event) error {
	// Return if there is no events to save
	if len(events) == 0 {
		return nil
	}

	// get bucket name from first event
	aggregateType := events[0].AggregateType
	aggregateID := events[0].AggregateID
	bucketName := aggregateKey(aggregateType, aggregateID)

	tx, err := e.db.Begin(true)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	evBucket := tx.Bucket([]byte(bucketName))
	if evBucket == nil {
		// Ensure that we have a bucket named events_aggregateType_aggregateID for the given aggregate
		err = e.createBucket([]byte(bucketName), tx)
		if err != nil {
			return errors.New("could not create aggregate events bucket")
		}
		evBucket = tx.Bucket([]byte(bucketName))
	}

	currentVersion := uint64(0)
	cursor := evBucket.Cursor()
	k, obj := cursor.Last()
	if k != nil {
		lastEvent := boltEvent{}
		err := json.Unmarshal(obj, &lastEvent)
		if err != nil {
			return errors.New(fmt.Sprintf("could not serialize event, %v", err))
		}
		currentVersion = lastEvent.Version
	}

	// Make sure no other has saved event to the same aggregate concurrently
	if core.Version(currentVersion)+1 != events[0].Version {
		return core.ErrConcurrency
	}

	globalBucket := tx.Bucket([]byte(globalEventOrderBucketName))
	if globalBucket == nil {
		return errors.New("global bucket not found")
	}

	var globalSequence uint64
	for i, event := range events {
		sequence, err := evBucket.NextSequence()
		if err != nil {
			return errors.New(fmt.Sprintf("could not get sequence for %#v", bucketName))
		}

		// We need to establish a global event order that spans over all buckets. This is so that we can be
		// able to play the event (or send) them in the order that they was entered into this database.
		// The global sequence bucket contains an ordered line of pointer to all events on the form bucket_name:seq_num
		globalSequence, err = globalBucket.NextSequence()
		if err != nil {
			return errors.New("could not get next sequence for global bucket")
		}

		// build the internal bolt event
		bEvent := boltEvent{
			AggregateID:   event.AggregateID,
			AggregateType: event.AggregateType,
			Version:       uint64(event.Version),
			GlobalVersion: globalSequence,
			Reason:        event.Reason,
			Timestamp:     event.Timestamp,
			Metadata:      event.Metadata,
			Data:          event.Data,
		}

		value, err := json.Marshal(bEvent)
		if err != nil {
			return errors.New(fmt.Sprintf("could not serialize event, %v", err))
		}

		err = evBucket.Put(itob(sequence), value)
		if err != nil {
			return errors.New(fmt.Sprintf("could not save event %#v in bucket", event))
		}
		err = globalBucket.Put(itob(globalSequence), value)
		if err != nil {
			return errors.New(fmt.Sprintf("could not save global sequence pointer for %#v", bucketName))
		}

		// override the event in the slice exposing the GlobalVersion to the caller
		events[i].GlobalVersion = core.Version(globalSequence)
	}
	return tx.Commit()
}

// Get aggregate events
func (e *BBolt) Get(ctx context.Context, id string, aggregateType string, afterVersion core.Version) (core.Iterator, error) {
	tx, err := e.db.Begin(false)
	if err != nil {
		return nil, err
	}
	bucketName := aggregateKey(aggregateType, id)
	bucket := tx.Bucket([]byte(bucketName))
	if bucket == nil {
		tx.Rollback()
		// no aggregate event stream
		return core.ZeroIterator{}, nil
	}
	cursor := bucket.Cursor()
	return &iterator{tx: tx, cursor: cursor, startPosition: position(afterVersion)}, nil

}

// GlobalEvents return count events in order globally from the start posistion
func (e *BBolt) GlobalEvents(start uint64) (core.Iterator, error) {
	tx, err := e.db.Begin(false)
	if err != nil {
		return nil, err
	}

	globalBucket := tx.Bucket([]byte(globalEventOrderBucketName))
	cursor := globalBucket.Cursor()

	return &iterator{tx: tx, cursor: cursor, startPosition: position(core.Version(start))}, nil
}

// Close closes the event stream and the underlying database
func (e *BBolt) Close() error {
	return e.db.Close()
}

// CreateBucket creates a bucket
func (e *BBolt) createBucket(bucketName []byte, tx *bbolt.Tx) error {
	// Ensure that we have a bucket named event_type for the given type
	if _, err := tx.CreateBucketIfNotExists([]byte(bucketName)); err != nil {
		return errors.New(fmt.Sprintf("could not create bucket for %s: %s", bucketName, err))
	}
	return nil

}

// aggregateKey generate a aggregate key to store events against from aggregateType and aggregateID
func aggregateKey(aggregateType, aggregateID string) string {
	return aggregateType + "_" + aggregateID
}

// calculate the correct posiotion and convert to bbolt key type
func position(p core.Version) []byte {
	return itob(uint64(p + 1))
}
