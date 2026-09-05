package runtime

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestSteeringReceiptIsDetachedMetadata(t *testing.T) {
	sequence := int64(5)
	m := SteeringMessage{Version: 1, ID: "message", TaskID: "task", Text: "sensitive guidance", State: "applied", CreatedAt: time.Now().UTC(), AppliedSequence: &sequence}
	receipt := m.Receipt()
	if receipt.Validate() != nil {
		t.Fatal("invalidreceipt")
	}
	body, err := json.Marshal(receipt)
	if err != nil || strings.Contains(string(body), "sensitive") || strings.Contains(string(body), "text") {
		t.Fatal(string(body), err)
	}
	sequence = 0
	if receipt.Validate() != nil || *receipt.AppliedSequence != 5 || m.Validate() == nil {
		t.Fatal("receipt aliases source")
	}
}
