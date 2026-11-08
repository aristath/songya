package songya

import (
	"context"
	"fmt"
	"testing"
)

func TestTimelineSnapshotDoesNotPageAwayOrderingLinksOrExpandDatePairs(t *testing.T) {
	catalog := Catalog{}
	for i := 0; i < 180; i++ {
		date := fmt.Sprintf("%04d-01-01", 1900+i)
		catalog.Records = append(catalog.Records, Record{ID: fmt.Sprintf("m_%012d", i), Source: fmt.Sprintf("memories/m_%012d/record.md", i), Kind: Memories, User: "reader", Title: fmt.Sprintf("Event %d", i), Time: RecordTime{Date: &date}})
	}
	catalog.Records[179].Relations = []Relation{{Type: "after", Target: catalog.Records[0].ID}}
	result, err := catalog.Timeline(context.Background(), Query{Kind: Memories, User: "reader"})
	if err != nil || result.Truncated || len(result.Records) != 180 || len(result.Constraints) != 1 {
		t.Fatal("snapshot lost records/links or expanded date pairs", len(result.Records), len(result.Constraints), err)
	}
	if result.Constraints[0].Before != catalog.Records[0].ID || result.Constraints[0].After != catalog.Records[179].ID {
		t.Fatal(result.Constraints)
	}
}
