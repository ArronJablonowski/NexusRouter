package releasepack

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"debug/elf"
	"debug/macho"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func signingFixture(t *testing.T) (dir, seedFile, publicFile string) {
	t.Helper()
	dir = t.TempDir()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keys := t.TempDir()
	seedFile, publicFile = filepath.Join(keys, "seed"), filepath.Join(keys, "public")
	writeSigningFixture(t, seedFile, []byte(hex.EncodeToString(private.Seed())+"\n"), 0600)
	writeSigningFixture(t, publicFile, []byte(hex.EncodeToString(public)+"\n"), 0644)
	var sums strings.Builder
	manifest := Manifest{SchemaVersion: releaseManifestSchema, Version: "1.0.0", Commit: strings.Repeat("a", 40), Created: "2026-09-13T18:00:00Z", Toolchain: "go1.27.1"}
	for _, target := range [][2]string{{"darwin", "amd64"}, {"darwin", "arm64"}, {"linux", "amd64"}, {"linux", "arm64"}} {
		name := "NexusRouter_1.0.0_" + target[0] + "_" + target[1] + ".tar.gz"
		body := signingArchiveFixture(t, target[0], target[1])
		writeSigningFixture(t, filepath.Join(dir, name), body, 0644)
		fmt.Fprintf(&sums, "%x  %s\n", sha256.Sum256(body), name)
		binary := signingBinaryFixture(t, target[0], target[1])
		sbom := signingSBOMFixture(t, manifest.Version, manifest.Commit, target[0], target[1], binary)
		_, metadata, err := releaseEntries(signingCollateralFixture(), signingNoticeFixture(target[0], target[1]), sbom, binary)
		if err != nil {
			t.Fatal(err)
		}
		manifest.Artifacts = append(manifest.Artifacts, Artifact{OS: target[0], Arch: target[1], File: name, SHA256: fmt.Sprintf("%x", sha256.Sum256(body)), Entries: metadata})
	}
	body, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	body = append(body, '\n')
	writeSigningFixture(t, filepath.Join(dir, "manifest.json"), body, 0644)
	fmt.Fprintf(&sums, "%x  manifest.json\n", sha256.Sum256(body))
	writeSigningFixture(t, filepath.Join(dir, "SHA256SUMS"), []byte(sums.String()), 0644)
	return
}

func signingArchiveFixture(t *testing.T, targetOS, targetArch string) []byte {
	t.Helper()
	binary := signingBinaryFixture(t, targetOS, targetArch)
	sbom := signingSBOMFixture(t, "1.0.0", strings.Repeat("a", 40), targetOS, targetArch, binary)
	entries, _, err := releaseEntries(signingCollateralFixture(), signingNoticeFixture(targetOS, targetArch), sbom, binary)
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if err := Archive(&archive, entries); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}

func signingCollateralFixture() collateral {
	return collateral{
		install: []byte("# Install fixture\n"),
		license: []byte("MIT License fixture\n"),
		notes:   []byte("# Release notes fixture\n"),
		config:  []byte("version: 1\nmode: local_only\n"),
	}
}

func signingNoticeFixture(targetOS, targetArch string) []byte {
	body, err := renderThirdPartyNotices(targetOS, targetArch, []noticeModule{{Path: "example.com/dependency", Version: "v1.0.0", Files: []noticeFile{{Name: "LICENSE", Body: []byte("fixture license\n")}}}})
	if err != nil {
		panic(err)
	}
	return body
}

func signingBinaryFixture(t *testing.T, targetOS, targetArch string) []byte {
	t.Helper()
	if targetOS == "darwin" {
		// A minimal 64-bit Mach-O executable with an executable __TEXT segment
		// and LC_UNIXTHREAD entry mechanism.
		body := make([]byte, 128)
		binary.LittleEndian.PutUint32(body[0:4], uint32(macho.Magic64))
		cpu := uint32(macho.CpuAmd64)
		if targetArch == "arm64" {
			cpu = uint32(macho.CpuArm64)
		}
		binary.LittleEndian.PutUint32(body[4:8], cpu)
		binary.LittleEndian.PutUint32(body[8:12], 3)
		binary.LittleEndian.PutUint32(body[12:16], uint32(macho.TypeExec))
		binary.LittleEndian.PutUint32(body[16:20], 2)
		binary.LittleEndian.PutUint32(body[20:24], 96)
		binary.LittleEndian.PutUint32(body[32:36], uint32(macho.LoadCmdSegment64))
		binary.LittleEndian.PutUint32(body[36:40], 72)
		copy(body[40:56], "__TEXT")
		binary.LittleEndian.PutUint64(body[56:64], 0x100000000)
		binary.LittleEndian.PutUint64(body[64:72], uint64(len(body)))
		binary.LittleEndian.PutUint64(body[80:88], uint64(len(body)))
		binary.LittleEndian.PutUint32(body[88:92], 7)
		binary.LittleEndian.PutUint32(body[92:96], 5)
		binary.LittleEndian.PutUint32(body[104:108], uint32(macho.LoadCmdUnixThread))
		binary.LittleEndian.PutUint32(body[108:112], 24)
		binary.LittleEndian.PutUint32(body[112:116], 1)
		return body
	}
	// A minimal 64-bit little-endian ELF executable with one executable PT_LOAD
	// segment and an entry point mapped by that segment.
	body := make([]byte, 121)
	copy(body, []byte{0x7f, 'E', 'L', 'F', byte(elf.ELFCLASS64), byte(elf.ELFDATA2LSB), byte(elf.EV_CURRENT)})
	binary.LittleEndian.PutUint16(body[16:18], uint16(elf.ET_EXEC))
	machine := uint16(elf.EM_X86_64)
	if targetArch == "arm64" {
		machine = uint16(elf.EM_AARCH64)
	}
	binary.LittleEndian.PutUint16(body[18:20], machine)
	binary.LittleEndian.PutUint32(body[20:24], uint32(elf.EV_CURRENT))
	binary.LittleEndian.PutUint64(body[24:32], 0x400078)
	binary.LittleEndian.PutUint64(body[32:40], 64)
	binary.LittleEndian.PutUint16(body[52:54], 64)
	binary.LittleEndian.PutUint16(body[54:56], 56)
	binary.LittleEndian.PutUint16(body[56:58], 1)
	binary.LittleEndian.PutUint32(body[64:68], uint32(elf.PT_LOAD))
	binary.LittleEndian.PutUint32(body[68:72], uint32(elf.PF_R|elf.PF_X))
	binary.LittleEndian.PutUint64(body[80:88], 0x400000)
	binary.LittleEndian.PutUint64(body[88:96], 0x400000)
	binary.LittleEndian.PutUint64(body[96:104], uint64(len(body)))
	binary.LittleEndian.PutUint64(body[104:112], uint64(len(body)))
	binary.LittleEndian.PutUint64(body[112:120], 0x1000)
	body[120] = 0xc3
	return body
}

func headerOnlyBinaryFixture(targetOS, targetArch string) []byte {
	var executable []byte
	if targetOS == "darwin" {
		executable = make([]byte, 32)
		binary.LittleEndian.PutUint32(executable[0:4], 0xfeedfacf)
		cpu := uint32(0x01000007)
		if targetArch == "arm64" {
			cpu = 0x0100000c
		}
		binary.LittleEndian.PutUint32(executable[4:8], cpu)
		binary.LittleEndian.PutUint32(executable[8:12], 3)
		binary.LittleEndian.PutUint32(executable[12:16], 2)
	} else {
		executable = make([]byte, 64)
		copy(executable, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
		binary.LittleEndian.PutUint16(executable[16:18], 2)
		machine := uint16(62)
		if targetArch == "arm64" {
			machine = 183
		}
		binary.LittleEndian.PutUint16(executable[18:20], machine)
		binary.LittleEndian.PutUint32(executable[20:24], 1)
		binary.LittleEndian.PutUint16(executable[52:54], 64)
		binary.LittleEndian.PutUint16(executable[54:56], 56)
		binary.LittleEndian.PutUint16(executable[58:60], 64)
	}
	return executable
}

func TestSigningManifestContract(t *testing.T) {
	for _, scenario := range []string{"schema", "version", "commit", "created", "toolchain", "target", "hash", "filename", "count", "entry_count", "entry_name", "entry_order", "entry_mode", "entry_size", "entry_hash", "shared_collateral", "malformed", "duplicate_key", "missing_manifest", "extra_payload"} {
		t.Run(scenario, func(t *testing.T) {
			dir, seedFile, public := signingFixture(t)
			path := filepath.Join(dir, "manifest.json")
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var manifest Manifest
			if err = json.Unmarshal(body, &manifest); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "schema":
				manifest.SchemaVersion = 1
			case "version":
				manifest.Version = "01.0.0"
			case "commit":
				manifest.Commit = strings.Repeat("A", 40)
			case "created":
				manifest.Created = "2026-09-13T18:00:01Z"
			case "toolchain":
				manifest.Toolchain = "go01.27.1"
			case "target":
				manifest.Artifacts[0].OS = "windows"
			case "hash":
				manifest.Artifacts[0].SHA256 = strings.Repeat("0", 64)
			case "filename":
				manifest.Artifacts[0].File = "different.tar.gz"
			case "count":
				manifest.Artifacts = manifest.Artifacts[:3]
			case "entry_count":
				manifest.Artifacts[0].Entries = manifest.Artifacts[0].Entries[:5]
			case "entry_name":
				manifest.Artifacts[0].Entries[0].Name = "OTHER.md"
			case "entry_order":
				manifest.Artifacts[0].Entries[0], manifest.Artifacts[0].Entries[1] = manifest.Artifacts[0].Entries[1], manifest.Artifacts[0].Entries[0]
			case "entry_mode":
				manifest.Artifacts[0].Entries[1].Mode = 0600
			case "entry_size":
				manifest.Artifacts[0].Entries[2].Size++
			case "entry_hash":
				manifest.Artifacts[0].Entries[4].SHA256 = strings.Repeat("0", 64)
			case "shared_collateral":
				manifest.Artifacts[1].Entries[0].SHA256 = strings.Repeat("1", 64)
			}
			body, err = json.MarshalIndent(manifest, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			body = append(body, '\n')
			if scenario == "malformed" {
				body = []byte("{invalid}")
			}
			if scenario == "duplicate_key" {
				body = []byte(strings.Replace(string(body), `"schema_version": 3,`, `"schema_version": 3, "schema_version": 3,`, 1))
			}
			writeSigningFixture(t, path, body, 0644)
			if scenario == "missing_manifest" {
				if err = os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "extra_payload" {
				writeSigningFixture(t, filepath.Join(dir, "extra.txt"), []byte("extra"), 0644)
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, entry := range entries {
				if entry.Name() != "SHA256SUMS" {
					names = append(names, entry.Name())
				}
			}
			sort.Strings(names)
			var sums strings.Builder
			for _, name := range names {
				body, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil {
					t.Fatal(err)
				}
				fmt.Fprintf(&sums, "%x  %s\n", sha256.Sum256(body), name)
			}
			writeSigningFixture(t, filepath.Join(dir, "SHA256SUMS"), []byte(sums.String()), 0644)
			if err := signUncheckedForTest(dir, seedFile); err != ErrSignature {
				t.Fatal("invalid contract signed", err)
			}
			// Even an authentic signature cannot turn malformed release metadata
			// into a conforming NexusRouter package.
			seed, err := signingKeyFile(seedFile, true)
			if err != nil {
				t.Fatal(err)
			}
			sig := ed25519.Sign(ed25519.NewKeyFromSeed(seed), []byte(sums.String()))
			writeSigningFixture(t, filepath.Join(dir, signatureName), []byte(hex.EncodeToString(sig)+"\n"), 0644)
			if err := Verify(dir, public); err != ErrSignature {
				t.Fatal("invalid authenticated contract accepted", err)
			}
		})
	}
}

func writeSigningFixture(t *testing.T, path string, body []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, body, mode); err != nil {
		t.Fatal(err)
	}
}

func TestSigningOfflineRoundTrip(t *testing.T) {
	dir, seed, public := signingFixture(t)
	if err := signUncheckedForTest(dir, seed); err != nil {
		t.Fatal(err)
	}
	if err := Verify(dir, public); err != nil {
		t.Fatal(err)
	}
	if err := signUncheckedForTest(dir, seed); err != ErrSignature {
		t.Fatal("existing signature overwritten", err)
	}
	_, _, wrong := signingFixture(t)
	if err := Verify(dir, wrong); err != ErrSignature {
		t.Fatal("untrusted key accepted", err)
	}
}

func TestSigningRejectsInvalidArchivePayloads(t *testing.T) {
	for _, scenario := range []string{"plain_string", "multiple_entries", "wrong_name", "missing_notice", "missing_sbom", "missing_config", "tampered_license", "tampered_sbom", "wrong_notice_target", "wrong_sbom_target", "wrong_sbom_binary", "wrong_sbom_toolchain", "noncanonical_metadata", "trailing_bytes", "bad_gzip_crc", "compressed_bomb", "wrong_os", "wrong_arch", "header_only_macho", "header_only_elf", "elf_interpreter"} {
		t.Run(scenario, func(t *testing.T) {
			dir, seed, public := signingFixture(t)
			name := "NexusRouter_1.0.0_darwin_amd64.tar.gz"
			var body []byte
			switch scenario {
			case "plain_string":
				// This was the former fixture shape: authenticated bytes, but
				// neither a gzip archive nor a NexusRouter executable.
				body = []byte("fixture " + name)
			case "multiple_entries":
				var archive bytes.Buffer
				if err := Archive(&archive, []Entry{{Name: "nexus", Data: signingBinaryFixture(t, "darwin", "amd64")}, {Name: noticeName, Data: signingNoticeFixture("darwin", "amd64")}, {Name: "extra", Data: []byte("extra")}}); err != nil {
					t.Fatal(err)
				}
				body = archive.Bytes()
			case "wrong_name":
				var archive bytes.Buffer
				if err := Archive(&archive, []Entry{{Name: "other", Data: signingBinaryFixture(t, "darwin", "amd64")}, {Name: noticeName, Data: signingNoticeFixture("darwin", "amd64")}}); err != nil {
					t.Fatal(err)
				}
				body = archive.Bytes()
			case "missing_notice":
				body = archiveSigningBody(t, signingBinaryFixture(t, "darwin", "amd64"), nil)
			case "missing_sbom", "missing_config", "tampered_license", "tampered_sbom", "wrong_sbom_target", "wrong_sbom_binary", "wrong_sbom_toolchain":
				binary := signingBinaryFixture(t, "darwin", "amd64")
				sbom := signingSBOMFixture(t, "1.0.0", strings.Repeat("a", 40), "darwin", "amd64", binary)
				entries, _, err := releaseEntries(signingCollateralFixture(), signingNoticeFixture("darwin", "amd64"), sbom, binary)
				if err != nil {
					t.Fatal(err)
				}
				switch scenario {
				case "missing_sbom":
					entries = append(entries[:3], entries[4:]...)
				case "missing_config":
					entries = append(entries[:5], entries[6:]...)
				case "tampered_license":
					entries[1].Data = []byte("changed license\n")
				case "tampered_sbom":
					entries[3].Data = append([]byte(nil), entries[3].Data...)
					entries[3].Data[len(entries[3].Data)-2] ^= 1
				case "wrong_sbom_target":
					entries[3].Data = driftApprovedSBOMFixture(t, entries[3].Data, "target")
				case "wrong_sbom_binary":
					entries[3].Data = driftApprovedSBOMFixture(t, entries[3].Data, "binary")
				case "wrong_sbom_toolchain":
					entries[3].Data = driftApprovedSBOMFixture(t, entries[3].Data, "toolchain")
				}
				var archive bytes.Buffer
				if err = Archive(&archive, entries); err != nil {
					t.Fatal(err)
				}
				body = archive.Bytes()
			case "wrong_notice_target":
				body = archiveSigningBody(t, signingBinaryFixture(t, "darwin", "amd64"), signingNoticeFixture("linux", "amd64"))
			case "noncanonical_metadata":
				body = customSigningArchive(t, signingBinaryFixture(t, "darwin", "amd64"), 0700, 0)
			case "trailing_bytes":
				body = customSigningArchive(t, signingBinaryFixture(t, "darwin", "amd64"), 0755, 8)
			case "bad_gzip_crc":
				body = signingArchiveFixture(t, "darwin", "amd64")
				body[len(body)-8] ^= 1
			case "compressed_bomb":
				body = customSigningArchive(t, signingBinaryFixture(t, "darwin", "amd64"), 0755, maxTarStream+1)
			case "wrong_os":
				body = signingArchiveFixture(t, "linux", "amd64")
			case "wrong_arch":
				body = signingArchiveFixture(t, "darwin", "arm64")
			case "header_only_macho":
				body = archiveSigningBody(t, headerOnlyBinaryFixture("darwin", "amd64"), signingNoticeFixture("darwin", "amd64"))
			case "header_only_elf":
				body = archiveSigningBody(t, headerOnlyBinaryFixture("linux", "amd64"), signingNoticeFixture("darwin", "amd64"))
			case "elf_interpreter":
				body = archiveSigningBody(t, signingELFInterpreterFixture(t), signingNoticeFixture("darwin", "amd64"))
			}
			writeSigningFixture(t, filepath.Join(dir, name), body, 0644)
			refreshSigningFixture(t, dir)
			if err := signUncheckedForTest(dir, seed); err != ErrSignature {
				t.Fatal("invalid archive signed", err)
			}
			authenticateSigningFixture(t, dir, seed)
			if err := Verify(dir, public); err != ErrSignature {
				t.Fatal("invalid authenticated archive verified", err)
			}
		})
	}
}

func archiveSigningBody(t *testing.T, body, notice []byte) []byte {
	t.Helper()
	entries := []Entry{{Name: "nexus", Data: body}}
	if notice != nil {
		var err error
		sbom := signingSBOMFixture(t, "1.0.0", strings.Repeat("a", 40), "darwin", "amd64", body)
		entries, _, err = releaseEntries(signingCollateralFixture(), notice, sbom, body)
		if err != nil {
			t.Fatal(err)
		}
	}
	var archive bytes.Buffer
	if err := Archive(&archive, entries); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}

func customSigningArchive(t *testing.T, body []byte, mode int64, suffix int64) []byte {
	t.Helper()
	sbom := signingSBOMFixture(t, "1.0.0", strings.Repeat("a", 40), "darwin", "amd64", body)
	entries, _, err := releaseEntries(signingCollateralFixture(), signingNoticeFixture("darwin", "amd64"), sbom, body)
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	gz.Header.ModTime = time.Time{}
	gz.Header.OS = 255
	tw := tar.NewWriter(gz)
	for _, entry := range entries {
		entryMode := int64(0644)
		if entry.Name == "nexus" {
			entryMode = mode
		}
		header := &tar.Header{Name: entry.Name, Mode: entryMode, Size: int64(len(entry.Data)), ModTime: time.Unix(0, 0), Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(entry.Data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	zeros := make([]byte, 1<<20)
	for suffix > 0 {
		chunk := int64(len(zeros))
		if chunk > suffix {
			chunk = suffix
		}
		if _, err := gz.Write(zeros[:chunk]); err != nil {
			t.Fatal(err)
		}
		suffix -= chunk
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}

func signingELFInterpreterFixture(t *testing.T) []byte {
	t.Helper()
	body := signingBinaryFixture(t, "linux", "amd64")
	body = append(body, make([]byte, 56)...)
	binary.LittleEndian.PutUint16(body[56:58], 2)
	offset := 64 + 56
	binary.LittleEndian.PutUint32(body[offset:offset+4], uint32(elf.PT_INTERP))
	binary.LittleEndian.PutUint32(body[offset+4:offset+8], uint32(elf.PF_R))
	binary.LittleEndian.PutUint64(body[offset+8:offset+16], 120)
	binary.LittleEndian.PutUint64(body[offset+32:offset+40], 1)
	binary.LittleEndian.PutUint64(body[offset+40:offset+48], 1)
	binary.LittleEndian.PutUint64(body[offset+48:offset+56], 1)
	return body
}

func refreshSigningFixture(t *testing.T, dir string) {
	t.Helper()
	manifestPath := filepath.Join(dir, "manifest.json")
	body, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	if err = json.Unmarshal(body, &manifest); err != nil {
		t.Fatal(err)
	}
	var sums strings.Builder
	for i := range manifest.Artifacts {
		artifactBody, readErr := os.ReadFile(filepath.Join(dir, manifest.Artifacts[i].File))
		if readErr != nil {
			t.Fatal(readErr)
		}
		digest := fmt.Sprintf("%x", sha256.Sum256(artifactBody))
		manifest.Artifacts[i].SHA256 = digest
		fmt.Fprintf(&sums, "%s  %s\n", digest, manifest.Artifacts[i].File)
	}
	body, err = json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	body = append(body, '\n')
	writeSigningFixture(t, manifestPath, body, 0644)
	fmt.Fprintf(&sums, "%x  manifest.json\n", sha256.Sum256(body))
	writeSigningFixture(t, filepath.Join(dir, "SHA256SUMS"), []byte(sums.String()), 0644)
}

func authenticateSigningFixture(t *testing.T, dir, seedFile string) {
	t.Helper()
	seed, err := signingKeyFile(seedFile, true)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(seed)
	sums, err := os.ReadFile(filepath.Join(dir, "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}
	signature := ed25519.Sign(ed25519.NewKeyFromSeed(seed), sums)
	writeSigningFixture(t, filepath.Join(dir, signatureName), []byte(hex.EncodeToString(signature)+"\n"), 0644)
}

func TestSigningTampering(t *testing.T) {
	for _, name := range []string{"NexusRouter_1.0.0_darwin_amd64.tar.gz", "manifest.json", "SHA256SUMS", "SHA256SUMS.sig"} {
		t.Run(name, func(t *testing.T) {
			dir, seed, public := signingFixture(t)
			if err := signUncheckedForTest(dir, seed); err != nil {
				t.Fatal(err)
			}
			writeSigningFixture(t, filepath.Join(dir, name), []byte("tampered"), 0644)
			if err := Verify(dir, public); err != ErrSignature {
				t.Fatal("tampering accepted", err)
			}
		})
	}
}

func TestSigningRejectsUnsafeFilesAndIncompleteCoverage(t *testing.T) {
	for _, scenario := range []string{"symlink_archive", "symlink_sums", "symlink_key", "directory_archive", "unlisted", "bad_permissions", "traversal", "duplicate", "unsorted", "no_newline"} {
		t.Run(scenario, func(t *testing.T) {
			dir, seed, _ := signingFixture(t)
			sumsPath := filepath.Join(dir, "SHA256SUMS")
			sums, err := os.ReadFile(sumsPath)
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "symlink_archive", "symlink_sums", "symlink_key":
				path := filepath.Join(dir, "NexusRouter_1.0.0_darwin_amd64.tar.gz")
				if scenario == "symlink_sums" {
					path = sumsPath
				} else if scenario == "symlink_key" {
					path = seed
				}
				target := filepath.Join(t.TempDir(), "target")
				if err := os.Rename(path, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			case "directory_archive":
				path := filepath.Join(dir, "NexusRouter_1.0.0_darwin_amd64.tar.gz")
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "unlisted":
				writeSigningFixture(t, filepath.Join(dir, "extra"), []byte("uncovered"), 0600)
			case "bad_permissions":
				if err := os.Chmod(seed, 0644); err != nil {
					t.Fatal(err)
				}
			case "traversal":
				sums = []byte(strings.ReplaceAll(string(sums), "NexusRouter_1.0.0_darwin_amd64.tar.gz", "../NexusRouter_1.0.0_darwin_amd64.tar.gz"))
			case "duplicate":
				sums = append(sums, sums...)
			case "unsorted":
				lines := strings.Split(strings.TrimSuffix(string(sums), "\n"), "\n")
				sums = []byte(lines[1] + "\n" + lines[0] + "\n")
			case "no_newline":
				sums = sums[:len(sums)-1]
			}
			if scenario == "traversal" || scenario == "duplicate" || scenario == "unsorted" || scenario == "no_newline" {
				writeSigningFixture(t, sumsPath, sums, 0644)
			}
			if err := signUncheckedForTest(dir, seed); err != ErrSignature {
				t.Fatal("unsafe release accepted", err)
			}
			if _, err := os.Lstat(filepath.Join(dir, signatureName)); !os.IsNotExist(err) {
				t.Fatal("invalid release created signature")
			}
		})
	}
}

func TestSigningVerificationSymlinksAndCoverage(t *testing.T) {
	for _, name := range []string{"NexusRouter_1.0.0_darwin_amd64.tar.gz", "manifest.json", "SHA256SUMS", "SHA256SUMS.sig", "public", "extra"} {
		t.Run(name, func(t *testing.T) {
			dir, seed, public := signingFixture(t)
			if err := signUncheckedForTest(dir, seed); err != nil {
				t.Fatal(err)
			}
			if name == "extra" {
				writeSigningFixture(t, filepath.Join(dir, name), []byte("unlisted"), 0600)
			} else {
				path := filepath.Join(dir, name)
				if name == "public" {
					path = public
				}
				target := filepath.Join(t.TempDir(), "target")
				if err := os.Rename(path, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			if err := Verify(dir, public); err != ErrSignature {
				t.Fatal("unsafe signed release accepted", err)
			}
		})
	}
}
