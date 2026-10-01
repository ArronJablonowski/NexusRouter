package remoteconfig

import "testing"

func TestAdvertisementValidation(t *testing.T) {
	for _, a := range []Advertisement{{}, {true, "en1", "node-a", 22}, {false, "en1", "node-a", 0}} {
		if a.Validate() != nil {
			t.Fatal(a)
		}
	}
	for _, a := range []Advertisement{{true, "", "node-a", 22}, {true, "en1", "", 22}, {true, "en1\n", "node-a", 22}, {true, "en1", "UPPER", 22}, {true, "en1", "bad..name", 22}, {false, "", "", -1}, {true, "en1", "node-a", 65536}} {
		if a.Validate() == nil {
			t.Fatal("invalid accepted", a)
		}
	}
}
