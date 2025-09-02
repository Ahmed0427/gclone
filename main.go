package main

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
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
	req.Header.Set("Content-Type", "x-git-upload-pack-request")

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

	// expected response:
	// 0008NAK\nPACK...
	if len(body) < 12 {
		return nil, fmt.Errorf("response too short: %d bytes", len(body))
	}
	if string(body[4:7]) != "NAK" {
		return nil, fmt.Errorf("missing NAK, got %q", body[4:7])
	}
	if string(body[8:12]) != "PACK" {
		return nil, fmt.Errorf("missing PACK, got %q", body[8:12])
	}
	return body[12:], nil
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
	fmt.Println(hex.Dump(pack))
}
