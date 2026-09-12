package main

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
)

func loadTokenFile(path string) (string, error) {
	if path == "" {
		return "", errors.New("token file path cannot be empty")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("stat token file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", fmt.Errorf("refusing non-regular token file: %s", path)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("token file must not be group/world accessible: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read token file: %w", err)
	}
	value := strings.TrimSpace(string(data))
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) == 0 {
		if err == nil {
			err = errors.New("token is empty")
		}
		return "", fmt.Errorf("decode token file: %w", err)
	}
	return hex.EncodeToString(decoded), nil
}

func tokenArgs(opts options) []string {
	if opts.tokenFile != "" {
		return []string{"-token-file", opts.tokenFile}
	}
	return []string{"-token-hex", opts.tokenHex}
}
