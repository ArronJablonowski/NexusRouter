package releasepack

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func candidateFixture(t *testing.T) CandidateRecord {
	t.Helper()
	record, err := candidateRecord("1.0.0-rc.3", "0123456789abcdef0123456789abcdef01234567", "2026-09-13T18:00:00Z", collateralSourceFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func candidateBody(t *testing.T, record CandidateRecord) []byte {
	t.Helper()
	body, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(body, '\n')
}

func TestCandidateCanonicalContract(t *testing.T) {
	record := candidateFixture(t)
	body := candidateBody(t, record)
	if validateCandidate(body) != nil {
		t.Fatal("canonical candidate rejected")
	}
	if record.SchemaVersion != candidateSchema || record.ReleaseManifestSchema != releaseManifestSchema || len(record.Targets) != 4 || len(record.ArchiveEntries) != 7 || len(record.SourceCollateral) != 4 || len(record.OperatorGates) != 5 {
		t.Fatal("candidate contract cardinality")
	}
	for _, target := range record.Targets {
		if target.Decision != "unapproved" {
			t.Fatal("candidate generator fabricated a target decision")
		}
	}
	for _, gate := range record.OperatorGates {
		if gate.Status != "unapproved" {
			t.Fatal("candidate generator fabricated operator approval")
		}
	}
}

func TestCandidateRejectsContractDrift(t *testing.T) {
	base := candidateFixture(t)
	tests := []struct {
		name   string
		mutate func(*CandidateRecord)
	}{
		{"schema", func(r *CandidateRecord) { r.SchemaVersion++ }},
		{"manifest_schema", func(r *CandidateRecord) { r.ReleaseManifestSchema++ }},
		{"version", func(r *CandidateRecord) { r.ReleaseVersion = "v1.0.0" }},
		{"commit", func(r *CandidateRecord) { r.SourceCommit = "ABC" }},
		{"created_format", func(r *CandidateRecord) { r.ReleaseCreated = "2026-09-13T12:00:00-06:00" }},
		{"target_removed", func(r *CandidateRecord) { r.Targets = r.Targets[:3] }},
		{"target_reordered", func(r *CandidateRecord) { r.Targets[0], r.Targets[1] = r.Targets[1], r.Targets[0] }},
		{"target_approved", func(r *CandidateRecord) { r.Targets[0].Decision = "supported" }},
		{"entry_removed", func(r *CandidateRecord) { r.ArchiveEntries = r.ArchiveEntries[:5] }},
		{"entry_mode", func(r *CandidateRecord) { r.ArchiveEntries[0].Mode = 0600 }},
		{"entry_bound", func(r *CandidateRecord) { r.ArchiveEntries[0].MaxBytes++ }},
		{"entry_sharing", func(r *CandidateRecord) { r.ArchiveEntries[3].Shared = true }},
		{"collateral_removed", func(r *CandidateRecord) { r.SourceCollateral = r.SourceCollateral[:3] }},
		{"collateral_source", func(r *CandidateRecord) { r.SourceCollateral[0].Source = "other" }},
		{"collateral_hash", func(r *CandidateRecord) { r.SourceCollateral[0].SHA256 = "bad" }},
		{"gate_removed", func(r *CandidateRecord) { r.OperatorGates = r.OperatorGates[:4] }},
		{"gate_approved", func(r *CandidateRecord) { r.OperatorGates[0].Status = "approved" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := base
			record.Targets = append([]CandidateTarget(nil), base.Targets...)
			record.ArchiveEntries = append([]CandidateEntry(nil), base.ArchiveEntries...)
			record.SourceCollateral = append([]CandidateCollateral(nil), base.SourceCollateral...)
			record.OperatorGates = append([]CandidateGate(nil), base.OperatorGates...)
			test.mutate(&record)
			if validateCandidate(candidateBody(t, record)) == nil {
				t.Fatal("mutated candidate accepted")
			}
		})
	}
}

func TestCandidateRejectsNoncanonicalEncoding(t *testing.T) {
	body := candidateBody(t, candidateFixture(t))
	for _, invalid := range [][]byte{
		body[:len(body)-1],
		append(append([]byte(nil), body...), '\n'),
		[]byte("{}\n"),
		append([]byte(" \n"), body...),
	} {
		if validateCandidate(invalid) == nil {
			t.Fatal("noncanonical candidate accepted")
		}
	}
}

func TestCandidateOutputMustBeOutsideSourceInExistingNonsymlinkParent(t *testing.T) {
	source := t.TempDir()
	inside := filepath.Join(source, "evidence")
	if err := os.Mkdir(inside, 0700); err != nil {
		t.Fatal(err)
	}
	if root, _, err := candidateOutputRoot(filepath.Join(inside, "candidate.json"), source); err == nil {
		root.Close()
		t.Fatal("candidate output inside source accepted")
	}
	parent := t.TempDir()
	if root, name, err := candidateOutputRoot(filepath.Join(parent, "candidate.json"), source); err != nil {
		t.Fatal(err)
	} else {
		root.Close()
		if name != "candidate.json" {
			t.Fatal("unexpected output name", name)
		}
	}
	missing := filepath.Join(parent, "missing", "candidate.json")
	if root, _, err := candidateOutputRoot(missing, source); err == nil {
		root.Close()
		t.Fatal("missing parent accepted")
	}
	link := filepath.Join(parent, "link")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	if root, _, err := candidateOutputRoot(filepath.Join(link, "candidate.json"), source); err == nil {
		root.Close()
		t.Fatal("symlink parent accepted")
	}
}

func TestFreezeAndVerifyCandidateFromCleanCommit(t *testing.T) {
	source := collateralSourceFixture(t)
	source, err := filepath.EvalSymlinks(source)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(source, "go.mod"), []byte("module example.com/candidate\n\ngo 1.27.1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	env := environment()
	for _, step := range [][]string{
		{"init"},
		{"config", "user.email", "candidate-test@example.invalid"},
		{"config", "user.name", "Candidate Test"},
		{"add", "."},
		{"commit", "-m", "candidate fixture"},
	} {
		if _, err := command(ctx, source, env, "git", step...); err != nil {
			t.Fatal(step, err)
		}
	}
	commit, err := command(ctx, source, env, "git", "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "candidate.json")
	options := Options{Version: "1.0.0-rc.3", Commit: commit, Source: source, Out: out}
	if err = FreezeCandidate(ctx, options); err != nil {
		t.Fatal(err)
	}
	if err = VerifyCandidate(ctx, out, source); err != nil {
		t.Fatal(err)
	}
	if err = FreezeCandidate(ctx, options); err == nil {
		t.Fatal("existing candidate overwritten")
	}
	body, record, err := readCandidateRecord(out)
	if err != nil {
		t.Fatal(err)
	}
	record.ReleaseCreated = "2026-09-13T18:00:01Z"
	if record.ReleaseCreated == candidateFixtureFromBody(t, body).ReleaseCreated {
		record.ReleaseCreated = "2026-09-13T18:00:02Z"
	}
	if err = os.WriteFile(out, candidateBody(t, record), 0644); err != nil {
		t.Fatal(err)
	}
	if err = VerifyCandidate(ctx, out, source); err == nil {
		t.Fatal("candidate timestamp drift from commit accepted")
	}
	if err = os.WriteFile(out, body, 0644); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(source, "untracked"), []byte("dirty\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = VerifyCandidate(ctx, out, source); err == nil {
		t.Fatal("dirty source accepted")
	}
}

func candidateFixtureFromBody(t *testing.T, body []byte) CandidateRecord {
	t.Helper()
	var record CandidateRecord
	if err := json.Unmarshal(body, &record); err != nil {
		t.Fatal(err)
	}
	return record
}
