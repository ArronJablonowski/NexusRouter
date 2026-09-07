package releasepack

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"debug/elf"
	"debug/macho"
	"encoding/binary"
	"io"
	"os"
	"time"
)

const maxArchive = maxArtifact + (1 << 20)
const maxTarStream = maxArtifact + 3*tarBlockSize
const tarBlockSize = 512

// validateReleaseArchive checks the format that Package emits before a payload
// can be signed or accepted. Checksums authenticate bytes; this check also
// ensures those bytes are a release artifact for the target named in the
// authenticated manifest.
func validateReleaseArchive(root *os.Root, artifact Artifact) error {
	body, err := readReleaseFile(root, artifact.File, maxArchive)
	if err != nil {
		return ErrSignature
	}
	compressed := bytes.NewReader(body)
	gz, err := gzip.NewReader(compressed)
	if err != nil || !gz.ModTime.IsZero() || gz.Name != "" || gz.Comment != "" || len(gz.Extra) != 0 || gz.OS != 255 {
		return ErrSignature
	}
	gz.Multistream(false)
	decompressed := &io.LimitedReader{R: gz, N: maxTarStream + 1}
	tr := tar.NewReader(decompressed)
	header, err := tr.Next()
	if err != nil || header.Name != "darwin" || header.Typeflag != tar.TypeReg || header.Mode != 0755 ||
		header.Size < 1 || header.Size > maxArtifact || header.Format != tar.FormatUSTAR ||
		header.Uid != 0 || header.Gid != 0 || header.Uname != "" || header.Gname != "" ||
		header.Linkname != "" || header.Devmajor != 0 || header.Devminor != 0 ||
		!header.ModTime.Equal(time.Unix(0, 0)) || !header.AccessTime.IsZero() || !header.ChangeTime.IsZero() ||
		len(header.PAXRecords) != 0 || len(header.Xattrs) != 0 {
		gz.Close()
		return ErrSignature
	}
	binary, err := io.ReadAll(io.LimitReader(tr, maxArtifact+1))
	if err != nil || int64(len(binary)) != header.Size {
		gz.Close()
		return ErrSignature
	}
	if _, err = tr.Next(); err != io.EOF {
		gz.Close()
		return ErrSignature
	}
	// tar.Reader reports EOF after the two canonical zero blocks. Drain through
	// gzip so its CRC/trailer is checked, but reject any uncompressed suffix and
	// cap the total work even for a highly compressed bomb.
	suffix, copyErr := io.Copy(io.Discard, decompressed)
	if copyErr != nil || suffix != 0 || decompressed.N == 0 {
		gz.Close()
		return ErrSignature
	}
	if err = gz.Close(); err != nil || compressed.Len() != 0 {
		return ErrSignature
	}
	return validateReleaseBinary(binary, artifact.OS, artifact.Arch)
}

func validateReleaseBinary(body []byte, targetOS, targetArch string) error {
	switch targetOS {
	case "darwin":
		file, err := macho.NewFile(bytes.NewReader(body))
		if err != nil {
			return ErrSignature
		}
		defer file.Close()
		cpu := macho.CpuAmd64
		if targetArch == "arm64" {
			cpu = macho.CpuArm64
		}
		text := file.Segment("__TEXT")
		if file.Magic != macho.Magic64 || file.ByteOrder != binary.LittleEndian || file.Type != macho.TypeExec || file.Cpu != cpu || text == nil || text.Filesz == 0 || text.Memsz < text.Filesz || text.Prot&4 == 0 || !machoEntry(file, text) {
			return ErrSignature
		}
	case "linux":
		file, err := elf.NewFile(bytes.NewReader(body))
		if err != nil {
			return ErrSignature
		}
		defer file.Close()
		machine := elf.EM_X86_64
		if targetArch == "arm64" {
			machine = elf.EM_AARCH64
		}
		if file.Class != elf.ELFCLASS64 || file.Data != elf.ELFDATA2LSB || file.ByteOrder != binary.LittleEndian || file.Version != elf.EV_CURRENT || file.OSABI != elf.ELFOSABI_NONE || file.Type != elf.ET_EXEC || file.Machine != machine || file.Entry == 0 {
			return ErrSignature
		}
		entryMapped := false
		for _, program := range file.Progs {
			if program.Type == elf.PT_INTERP {
				return ErrSignature
			}
			if program.Type == elf.PT_LOAD && program.Flags&elf.PF_X != 0 && program.Filesz > 0 && program.Memsz >= program.Filesz && file.Entry >= program.Vaddr && file.Entry-program.Vaddr < program.Memsz {
				entryMapped = true
			}
		}
		if !entryMapped {
			return ErrSignature
		}
	default:
		return ErrSignature
	}
	return nil
}

func machoEntry(file *macho.File, text *macho.Segment) bool {
	for _, load := range file.Loads {
		raw := load.Raw()
		if len(raw) < 8 {
			continue
		}
		command := macho.LoadCmd(file.ByteOrder.Uint32(raw[:4]))
		if command == macho.LoadCmdThread || command == macho.LoadCmdUnixThread {
			if len(raw) > 16 {
				return true
			}
		}
		if command == 0x80000028 && len(raw) >= 24 { // LC_MAIN
			entryOffset := file.ByteOrder.Uint64(raw[8:16])
			return entryOffset < text.Filesz
		}
	}
	return false
}
