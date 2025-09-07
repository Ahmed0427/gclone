package main

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// all object types
const (
	OBJ_COMMIT    = 1
	OBJ_TREE      = 2
	OBJ_BLOB      = 3
	OBJ_TAG       = 4
	OBJ_OFS_DELTA = 6
	OBJ_REF_DELTA = 7
)

// all object types i need
var objTypeNames = map[byte]string{
	OBJ_COMMIT:    "commit",
	OBJ_TREE:      "tree",
	OBJ_BLOB:      "blob",
	OBJ_REF_DELTA: "ref_delta",
}

type Delta struct {
	hash string
	data []byte
}

const (
	INST_TYPE_ADD  = 0
	INST_TYPE_COPY = 1
)

type Instruction struct {
	instType byte
	size     int64
	offset   int64
	data     []byte
}

func getMainHash(repoURL string) (string, string, error) {
	refsURL := fmt.Sprintf("%s/info/refs?service=git-upload-pack", repoURL)

	resp, err := http.Get(refsURL)
	if err != nil {
		return "", "", fmt.Errorf("failed to perform GET request to %s: %w",
			refsURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return "", "", fmt.Errorf("unexpected status code %d while fetching %s",
			resp.StatusCode, refsURL)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", fmt.Errorf("failed to read response body from %s: %w",
			refsURL, err)
	}

	defaultBranch := ""
	lines := strings.Split(string(body), "\n")
	for _, line := range lines {
		if defaultBranch == "" {
			words := strings.Split(line, " ")
			for _, w := range words {
				if strings.Contains(w, "HEAD:refs/heads") {
					parts := strings.Split(w, "/")
					if len(parts) >= 3 {
						defaultBranch = parts[2]
					}
				}
			}
		} else {
			if strings.HasSuffix(line, fmt.Sprintf("refs/heads/%s", defaultBranch)) {
				fields := strings.Fields(line)
				if len(fields) > 0 && len(fields[0]) > 4 {
					return fields[0][4:], defaultBranch, nil
				}
			}
		}
	}
	return "", "", fmt.Errorf("Default branch hash not found")
}

func getPackfile(repoURL, mainHash string) ([]byte, error) {
	fetchURL := fmt.Sprintf("%s/git-upload-pack", repoURL)
	reqBody := []byte(fmt.Sprintf("0032want %s\n", mainHash) + "0000" + "0009done\n")

	req, err := http.NewRequest("POST", fetchURL, bytes.NewBuffer(reqBody))
	if err != nil {
		return []byte{}, fmt.Errorf("ERROR from http.NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-git-upload-pack-request")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return []byte{}, fmt.Errorf("ERROR from client.Do: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return []byte{}, fmt.Errorf("ERROR from io.ReadAll: %v", err)
	}

	if len(body) < 8 {
		return nil, fmt.Errorf("response body too short: %d bytes", len(body))
	}
	if string(body[4:7]) != "NAK" {
		return nil, fmt.Errorf("missing NAK, got %q", body[4:7])
	}
	return body[8:], nil
}

func compressBytes(data []byte) ([]byte, error) {
	var buf bytes.Buffer

	w := zlib.NewWriter(&buf)

	_, err := w.Write(data)
	if err != nil {
		return nil, err
	}

	if err := w.Close(); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

func decompressBytes(data []byte) ([]byte, error) {
	buf := bytes.NewReader(data)
	r, err := zlib.NewReader(buf)
	if err != nil {
		return nil, err
	}
	defer r.Close()

	var out bytes.Buffer
	_, err = out.ReadFrom(r)
	if err != nil {
		return nil, err
	}

	return out.Bytes(), nil
}

func writeObject(content []byte, objType string) error {
	// The object format is:
	// <type> <size>\0<content>

	size := len(content)
	header := fmt.Sprintf("%s %d", objType, size)
	objContent := append([]byte{}, []byte(header)...)
	objContent = append(objContent, 0x00)
	objContent = append(objContent, content...)

	hasher := sha1.New()
	hasher.Write(objContent)
	hash := hex.EncodeToString(hasher.Sum(nil))

	objDirPath := filepath.Join(".git/objects", hash[:2])
	err := os.MkdirAll(objDirPath, 0755)
	if err != nil {
		return fmt.Errorf("failed to create %s: %w", objDirPath, err)
	}
	objPath := filepath.Join(objDirPath, hash[2:])

	objContent, err = compressBytes(objContent)
	if err != nil {
		return fmt.Errorf("failed to compress object content: %w", err)
	}

	if err := os.WriteFile(objPath, objContent, 0644); err != nil {
		return fmt.Errorf("failed to write to %s: %w", objPath, err)
	}
	return nil
}

func readObject(hash string) ([]byte, string, error) {
	// The object format is:
	// <type> <size>\0<content>
	objPath := filepath.Join(".git/objects", hash[:2], hash[2:])

	objContent, err := os.ReadFile(objPath)
	if err != nil {
		return []byte{}, "", fmt.Errorf("failed to read obj: %w", err)
	}

	objContent, err = decompressBytes(objContent)
	if err != nil {
		return []byte{}, "", fmt.Errorf("failed to decompress object content: %w", err)
	}

	nullIdx := bytes.IndexByte(objContent, 0)
	if nullIdx == -1 {
		return []byte{}, "",
			fmt.Errorf("failed to find null byte in object file: %s", objPath)
	}

	header := objContent[:nullIdx]
	content := objContent[nullIdx+1:]

	spaceIdx := bytes.IndexByte(header, byte(' '))
	if spaceIdx == -1 {
		return []byte{}, "",
			fmt.Errorf("failed to find space byte in object file: %s", objPath)
	}

	objType := string(header[:spaceIdx])

	size, err := strconv.Atoi(string(header[spaceIdx+1:]))
	if err != nil {
		return nil, "", fmt.Errorf("invalid size in object header: %w", err)
	}

	if size != len(content) {
		return nil, "",
			fmt.Errorf("size mismatch: declared %d, got %d",
				size, len(content))
	}

	return content, objType, nil
}

func getObjectsCount(pack []byte) (uint32, error) {
	if len(pack) < 12 {
		return 0, fmt.Errorf("packfile too short: %d bytes", len(pack))
	}
	if string(pack[:4]) != "PACK" {
		return 0, fmt.Errorf(" Bad packfile format: missing 'PACK' in header")
	}
	return binary.BigEndian.Uint32(pack[8:12]), nil
}

func verifyChecksum(pack []byte) bool {
	packLen := len(pack)
	if packLen < 20 {
		return false
	}
	expectedChecksum := pack[packLen-20:]

	hash := sha1.New()
	hash.Write(pack[:packLen-20])
	calculatedChecksum := hash.Sum(nil)

	return bytes.Equal(expectedChecksum, calculatedChecksum)
}

func parsePackfile(pack []byte) ([]Delta, error) {
	if !verifyChecksum(pack) {
		return []Delta{}, fmt.Errorf("Checksum verification failed")
	}

	objsCount, err := getObjectsCount(pack)
	if err != nil {
		return []Delta{}, err
	}

	// skip pack header and checksum
	pack = pack[12 : len(pack)-20]

	deltas := []Delta{}

	off := int64(0)
	for i := uint32(0); i < objsCount; i++ {
		if off >= int64(len(pack)) {
			return []Delta{}, fmt.Errorf(
				"unexpected end of packfile at offset %d",
				off,
			)
		}

		byt := pack[off]
		off++

		objType := (byt >> 4) & 0x7
		if _, ok := objTypeNames[objType]; !ok {
			return []Delta{},
				fmt.Errorf("Bad object type in the packfile: %d", objType)
		}

		objSize := int64(byt & 0xF)
		shift := 4

		if (byt & 0x80) != 0 {
			for {
				if off >= int64(len(pack)) {
					return []Delta{}, fmt.Errorf(
						"unexpected end of packfile at offset %d",
						off,
					)
				}
				byt = pack[off]
				off++

				if shift > 60 {
					return []Delta{}, fmt.Errorf(
						"object size encoding too large at offset %d",
						off-1,
					)
				}
				objSize |= int64((int64(byt & 0x7F)) << shift)
				shift += 7

				if (byt & 0x80) == 0 {
					break
				}
			}
		}

		refDeltaHash := []byte{}
		if objType == OBJ_REF_DELTA {
			if off+20 > int64(len(pack)) {
				return []Delta{},
					fmt.Errorf(
						"unexpected end of packfile while reading ref delta hash",
					)
			}
			refDeltaHash = pack[off : off+20]
			off += 20
		}

		if off >= int64(len(pack)) {
			return []Delta{}, fmt.Errorf(
				"unexpected end of packfile at offset %d",
				off,
			)
		}

		bytesReader := bytes.NewReader(pack[off:])
		zlibReader, err := zlib.NewReader(bytesReader)
		if err != nil {
			return []Delta{}, fmt.Errorf("zlib.NewReader has failed: %v", err)
		}

		raw, err := io.ReadAll(zlibReader)
		zlibReader.Close()
		if err != nil {
			return []Delta{}, fmt.Errorf("io.ReadAll has failed: %v", err)
		}

		if int64(len(raw)) != objSize {
			return []Delta{}, fmt.Errorf(
				"object size mismatch: expected %d bytes, got %d bytes",
				objSize, len(raw),
			)
		}

		off += bytesReader.Size() - int64(bytesReader.Len())

		if objType == OBJ_REF_DELTA {
			deltas = append(deltas, Delta{
				hash: hex.EncodeToString(refDeltaHash),
				data: raw,
			})
		} else {
			err = writeObject(raw, objTypeNames[objType])
			if err != nil {
				return []Delta{}, fmt.Errorf("failed to write object: %w", err)
			}
		}
	}
	return deltas, nil
}

func parseVarInt(data []byte) (int64, int64, error) {
	var shift int8
	var off, value int64
	for {
		if off >= int64(len(data)) {
			return 0, 0, fmt.Errorf(
				"unexpected end of data at %d",
				off,
			)
		}
		byt := data[off]
		off++

		if shift > 60 {
			return 0, 0, fmt.Errorf(
				"object size encoding too large at %d",
				off-1,
			)
		}
		value |= int64((int64(byt & 0x7F)) << shift)
		shift += 7

		if (byt & 0x80) == 0 {
			break
		}
	}
	return value, off, nil
}

func parseInstructions(data []byte) ([]Instruction, int64) {
	off := int64(0)
	insts := []Instruction{}
	for off < int64(len(data)) {
		byt := data[off]
		off++

		inst := Instruction{}
		if byt&0x80 != 0 {
			inst.instType = INST_TYPE_COPY

			sizeBits := (byt >> 4) & 0x7
			offsetBits := byt & 0xF

			var offset int64 = 0
			for i := 0; i < 4; i++ {
				if (offsetBits & (1 << i)) != 0 {
					offset |= int64(data[off]) << (8 * i)
					off++
				}
			}

			var size int64 = 0
			for i := 0; i < 3; i++ {
				if (sizeBits & (1 << i)) != 0 {
					size |= int64(data[off]) << (8 * i)
					off++
				}
			}

			if size == 0 {
				size = 0x10000
			}

			inst.offset = offset
			inst.size = size

		} else {
			inst.instType = INST_TYPE_ADD
			inst.size = int64(byt & 0x7F)
			inst.data = data[off : off+inst.size]
			off += inst.size
		}
		insts = append(insts, inst)
	}
	return insts, off
}

func processRefDeltaObjs(deltas []Delta) error {
	for _, d := range deltas {
		off := int64(0)
		srcSize, read, err := parseVarInt(d.data[off:])
		if err != nil {
			return fmt.Errorf("failed to parse delta source size: %w", err)
		}
		off += read
		trgSize, read, err := parseVarInt(d.data[off:])
		if err != nil {
			return fmt.Errorf("failed to parse delta target size: %w", err)
		}
		off += read

		srcContent, objType, err := readObject(d.hash)
		if err != nil {
			return err
		}

		if int64(len(srcContent)) != srcSize {
			return fmt.Errorf(
				"delta source size(%d) doesn't match source object content size(%d)",
				srcSize, len(srcContent),
			)
		}

		insts, read := parseInstructions(d.data[off:])
		off += read

		trgContent := []byte{}
		for _, inst := range insts {
			if inst.instType == INST_TYPE_COPY {
				if inst.offset+inst.size > int64(len(srcContent)) {
					return fmt.Errorf(
						"instruction offset + size exceeds source content size",
					)
				}
				trgContent = append(trgContent,
					srcContent[inst.offset:inst.offset+inst.size]...)
			} else {
				if inst.size != int64(len(inst.data)) {
					return fmt.Errorf(
						"instruction size != instruction data size",
					)
				}
				trgContent = append(trgContent, inst.data...)
			}
		}

		if int64(len(trgContent)) != trgSize {
			return fmt.Errorf(
				"delta target size(%d) doesn't match target object content size(%d)",
				trgSize, len(trgContent),
			)
		}

		err = writeObject(trgContent, objType)
		if err != nil {
			return err
		}

	}
	return nil
}

func changeDir(dirPath string) error {
	err := os.MkdirAll(dirPath, 0755)
	if err != nil {
		return fmt.Errorf("failed to create %s: %w", dirPath, err)
	}

	err = os.Chdir(dirPath)
	if err != nil {
		return fmt.Errorf("failed to change to %s: %w", dirPath, err)
	}

	return nil
}

func initRepo(mainHash, defaultBranch string) error {
	gitDirs := []string{
		".git",
		".git/objects",
		".git/refs",
		".git/refs/heads",
	}

	for _, dir := range gitDirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create %s: %w", dir, err)
		}
	}

	headPath := filepath.Join(".git", "HEAD")
	headContent := []byte(fmt.Sprintf("ref: refs/heads/%s\n", defaultBranch))
	if err := os.WriteFile(headPath, headContent, 0644); err != nil {
		return fmt.Errorf("failed to write to %s: %w", headPath, err)
	}

	branchPath := filepath.Join(".git", "refs", "heads", defaultBranch)
	branchContent := []byte(mainHash + "\n")
	if err := os.WriteFile(branchPath, branchContent, 0644); err != nil {
		return fmt.Errorf("failed to write to %s: %w", branchPath, err)
	}

	return nil
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintf(os.Stderr, "%s <repo_url> <dir_path>\n", os.Args[0])
		os.Exit(1)
	}

	repoURL := os.Args[1]
	dirPath := os.Args[2]

	if err := changeDir(dirPath); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	mainHash, defaultBranch, err := getMainHash(repoURL)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	pack, err := getPackfile(repoURL, mainHash)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	if err := initRepo(mainHash, defaultBranch); err != nil {
		fmt.Println("failed to init repo:", err)
		os.Exit(1)
	}

	deltas, err := parsePackfile(pack)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	err = processRefDeltaObjs(deltas)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
