package main

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// Object types
const (
	OBJ_COMMIT    = 1
	OBJ_TREE      = 2
	OBJ_BLOB      = 3
	OBJ_TAG       = 4
	OBJ_OFS_DELTA = 6
	OBJ_REF_DELTA = 7
)

func getMainHash(URL string) (string, error) {
	refsURL := fmt.Sprintf("%s/info/refs?service=git-upload-pack", URL)

	resp, err := http.Get(refsURL)
	if err != nil {
		return "", fmt.Errorf("ERROR from http.Get: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("ERROR from io.ReadAll: %v", err)
	}

	lines := strings.Split(string(body), "\n")
	for _, line := range lines {
		if strings.HasSuffix(line, "refs/heads/main") ||
			strings.HasSuffix(line, "refs/heads/master") {
			return strings.Fields(line)[0][4:], nil
		}
	}

	return "", fmt.Errorf("main branch ref not found")
}

func getPackfile(URL string) ([]byte, error) {
	mainHash, err := getMainHash(URL)
	if err != nil {
		return []byte{}, err
	}

	fetchURL := fmt.Sprintf("%s/git-upload-pack", URL)
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

func parsePackfile(pack []byte) error {
	if !verifyChecksum(pack) {
		return fmt.Errorf("Checksum verification failed")
	}

	objsCount, err := getObjectsCount(pack)
	if err != nil {
		return err
	}

	// skip pack header and checksum
	pack = pack[12 : len(pack)-20]

	off := int64(0)
	for i := uint32(0); i < objsCount; i++ {
		if off >= int64(len(pack)) {
			return fmt.Errorf(
				"unexpected end of packfile at offset %d",
				off,
			)
		}

		byt := pack[off]
		off++

		objType := (byt >> 4) & 0x7
		if objType >= 7 || objType <= 0 || objType == 5 {
			return fmt.Errorf("Bad object type in the packfile: %d", objType)
		}

		objSize := int64(byt & 0xF)
		shift := 4

		if (byt & 0x80) != 0 {
			for {
				if off >= int64(len(pack)) {
					return fmt.Errorf(
						"unexpected end of packfile at offset %d",
						off,
					)
				}
				byt = pack[off]
				off++

				if shift > 60 {
					return fmt.Errorf(
						"object size encoding too large at offset %d",
						off-1,
					)
				}
				objSize += int64((int64(byt & 0x7F)) << shift)
				shift += 7

				if (byt & 0x80) == 0 {
					break
				}
			}
		}

		if objType == OBJ_REF_DELTA {
			off += 20
		}

		bytesReader := bytes.NewReader(pack[off:])
		zlibReader, err := zlib.NewReader(bytesReader)
		if err != nil {
			return fmt.Errorf("zlib.NewReader has failed: %v", err)
		}

		raw, err := io.ReadAll(zlibReader)
		zlibReader.Close()
		if err != nil {
			return fmt.Errorf("io.ReadAll has failed: %v", err)
		}

		if int64(len(raw)) != objSize {
			return fmt.Errorf(
				"object size mismatch: expected %d bytes, got %d bytes",
				objSize, len(raw),
			)
		}

		// Move offset by the compressed data size (difference before and after)
		off += bytesReader.Size() - int64(bytesReader.Len())
	}

	return nil
}

func writeToFile(data []byte) error {
	file, err := os.OpenFile("commit.pack", os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}

	_, err = file.Write(data)
	if err != nil {
		return err
	}

	return nil
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintf(os.Stderr, "%s <repo-url>\n", os.Args[0])
		os.Exit(1)
	}

	URL := os.Args[1]
	pack, err := getPackfile(URL)
	if err != nil {
		fmt.Println(err)
	}

	fmt.Println(parsePackfile(pack))
}
