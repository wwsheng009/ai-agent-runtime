package output

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEventHubJournalRingKeepsNewestSuffixInOrder(t *testing.T) {
	hub := newEventHub(JournalLimit{MaxItems: 3, MaxBytes: 1 << 20}, 1, 1)

	for index := 1; index <= 5; index++ {
		result := hub.submit(OutputEvent{
			Kind:    EventGatewayClosed,
			BatchID: fmt.Sprintf("batch-%d", index),
		})
		require.Equal(t, uint64(index), result.EventSequence)
	}

	events := hub.snapshotEvents(10)
	require.Equal(t, []string{"batch-3", "batch-4", "batch-5"}, eventBatchIDs(events))
	require.Equal(t, []uint64{3, 4, 5}, eventSequences(events))
	require.Equal(t, uint64(2), hub.getJournalDrops())
}

func TestEventHubJournalByteBudgetEvictsOldest(t *testing.T) {
	hub := newEventHub(JournalLimit{MaxItems: 100, MaxBytes: 140}, 1, 1)
	require.Equal(t, uint64(1), hub.submit(OutputEvent{BatchID: "keep-1"}).EventSequence)
	require.Equal(t, uint64(2), hub.submit(OutputEvent{BatchID: "keep-2"}).EventSequence)

	events := hub.snapshotEvents(10)
	require.Equal(t, []string{"keep-2"}, eventBatchIDs(events))
	require.Equal(t, uint64(1), hub.getJournalDrops())
}

func TestEventHubSnapshotDetachesPointerFields(t *testing.T) {
	hub := newEventHub(JournalLimit{MaxItems: 4, MaxBytes: 1 << 20}, 1, 1)
	history := &HistoryDeliveryDomain{ProjectionTargetID: "pt", HistoryEpoch: 7}
	hub.submit(OutputEvent{Kind: EventPrimaryStarted, History: history})

	events := hub.snapshotEvents(1)
	require.Len(t, events, 1)
	require.NotNil(t, events[0].History)
	events[0].History.HistoryEpoch = 99

	events = hub.snapshotEvents(1)
	require.Len(t, events, 1)
	require.NotNil(t, events[0].History)
	require.Equal(t, uint64(7), events[0].History.HistoryEpoch)
}

func eventBatchIDs(events []OutputEvent) []string {
	result := make([]string, len(events))
	for index, event := range events {
		result[index] = event.BatchID
	}
	return result
}

func eventSequences(events []OutputEvent) []uint64 {
	result := make([]uint64, len(events))
	for index, event := range events {
		result[index] = event.EventSequence
	}
	return result
}
