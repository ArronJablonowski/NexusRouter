package skills

import (
	"context"
	"testing"
)

func TestInventoryIncludesDraftsAndActiveVersions(t *testing.T) {
	s := openTest(t, testPath(t))
	ctx := context.Background()
	v, err := s.Draft(ctx, sample(), false)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Inventory(ctx, "project")
	if err != nil || len(got) != 1 || got[0].Status != "draft" {
		t.Fatal(got, err)
	}
	if err = s.Activate(ctx, v.Draft.Key, v.ID, "", pass, false); err != nil {
		t.Fatal(err)
	}
	d := sample()
	d.Description = "Second draft"
	_, err = s.Draft(ctx, d, false)
	if err != nil {
		t.Fatal(err)
	}
	got, err = s.Inventory(ctx, "project")
	if err != nil || len(got) != 2 {
		t.Fatal(got, err)
	}
	counts := map[string]int{}
	for _, item := range got {
		counts[item.Status]++
	}
	if counts["active"] != 1 || counts["draft"] != 1 {
		t.Fatal(counts)
	}
	if _, err = s.Inventory(ctx, "outside"); err == nil {
		t.Fatal("scope escaped")
	}
}
