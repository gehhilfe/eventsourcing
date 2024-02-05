package eventsourcing

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/hallgren/eventsourcing/core"
	"github.com/hallgren/eventsourcing/internal"
)

// ErrUnsavedEvents aggregate events must be saved before creating snapshot
var ErrUnsavedEvents = errors.New("aggregate holds unsaved events")

// SnapshotAggregate interface is used to serialize an aggregate that has no exported properties
type SnapshotAggregate interface {
	SerializeSnapshot(SerializeFunc) ([]byte, error)
	DeserializeSnapshot(DeserializeFunc, []byte) error
}

type SnapshotRepository struct {
	eventRepository *EventRepository
	snapshotStore   core.SnapshotStore
	Serializer      SerializeFunc
	Deserializer    DeserializeFunc
}

// NewSnapshotRepository factory function
func NewSnapshotRepository(snapshotStore core.SnapshotStore, eventRepo *EventRepository) *SnapshotRepository {
	return &SnapshotRepository{
		snapshotStore:   snapshotStore,
		eventRepository: eventRepo,
		Serializer:      json.Marshal,
		Deserializer:    json.Unmarshal,
	}
}

// Register register the aggregate in the event repository
func (s *SnapshotRepository) Register(a aggregate) {
	s.eventRepository.Register(a)
}

// EventRepository returns the underlying event repository. If the user wants to operate on the event repository
// and not use snapshot
func (s *SnapshotRepository) EventRepository() *EventRepository {
	return s.eventRepository
}

func (s *SnapshotRepository) GetWithContext(ctx context.Context, id string, a aggregate) error {
	if reflect.ValueOf(a).Kind() != reflect.Ptr {
		return ErrAggregateNeedsToBeAPointer
	}

	err := s.getSnapshot(ctx, id, a)
	if err != nil && !errors.Is(err, core.ErrSnapshotNotFound) {
		return err
	}

	// Append events that could have been saved after the snapshot
	return s.eventRepository.GetWithContext(ctx, id, a)
}

// GetSnapshot returns aggregate that is based on the snapshot data
// Beware that it could be more events that has happened after the snapshot was taken
func (s *SnapshotRepository) GetSnapshot(ctx context.Context, id string, a aggregate) error {
	if reflect.ValueOf(a).Kind() != reflect.Ptr {
		return ErrAggregateNeedsToBeAPointer
	}
	err := s.getSnapshot(ctx, id, a)
	if err != nil && errors.Is(err, core.ErrSnapshotNotFound) {
		return ErrAggregateNotFound
	}
	return err
}

func (s *SnapshotRepository) getSnapshot(ctx context.Context, id string, a aggregate) error {
	snapshot, err := s.snapshotStore.Get(ctx, id, internal.AggregateType(a))
	if err != nil {
		return err
	}

	// Does the aggregate have specific snapshot handling
	sa, ok := a.(SnapshotAggregate)
	if ok {
		err = sa.DeserializeSnapshot(s.Deserializer, snapshot.State)
		if err != nil {
			return err
		}
	} else {
		err = s.Deserializer(snapshot.State, a)
		if err != nil {
			return err
		}
	}

	// set the internal aggregate properties
	root := a.Root()
	root.aggregateGlobalVersion = Version(snapshot.GlobalVersion)
	root.aggregateVersion = Version(snapshot.Version)
	root.aggregateID = snapshot.ID

	return nil
}

// Save will save aggregate events and snapshot
func (s *SnapshotRepository) Save(a aggregate) error {
	// make sure events are stored
	err := s.eventRepository.Save(a)
	if err != nil {
		return err
	}

	return s.SaveSnapshot(a)
}

// SaveSnapshot will only store the snapshot and will return an error if there are events that are not stored
func (s *SnapshotRepository) SaveSnapshot(a aggregate) error {
	root := a.Root()
	if len(root.Events()) > 0 {
		return ErrUnsavedEvents
	}

	state := []byte{}
	var err error
	// Does the aggregate have specific snapshot handling
	sa, ok := a.(SnapshotAggregate)
	if ok {
		state, err = sa.SerializeSnapshot(s.Serializer)
		if err != nil {
			return err
		}
	} else {
		state, err = s.Serializer(a)
		if err != nil {
			return err
		}
	}

	snapshot := core.Snapshot{
		ID:            root.ID(),
		Type:          internal.AggregateType(a),
		Version:       core.Version(root.Version()),
		GlobalVersion: core.Version(root.GlobalVersion()),
		State:         state,
	}

	err = s.snapshotStore.Save(snapshot)
	return err
}
