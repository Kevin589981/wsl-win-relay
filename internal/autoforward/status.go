package autoforward

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"time"
)

const (
	StatusVersion           = 1
	MaxStatusDocumentSize   = 1 << 20
	StatusHeartbeatInterval = 30 * time.Second
	DefaultStatusMaxAge     = 2 * time.Minute
)

// ReadStatus reads and validates an atomic automatic-mapping status snapshot.
// It rejects links and oversized documents before decoding untrusted content.
func ReadStatus(path string) (StatusDocument, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return StatusDocument{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return StatusDocument{}, fmt.Errorf("status path is not a regular file: %s", path)
	}
	if info.Size() > MaxStatusDocumentSize {
		return StatusDocument{}, fmt.Errorf("status document exceeds %d bytes", MaxStatusDocumentSize)
	}
	file, err := os.Open(path)
	if err != nil {
		return StatusDocument{}, err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return StatusDocument{}, err
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return StatusDocument{}, errors.New("status file changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxStatusDocumentSize+1))
	if err != nil {
		return StatusDocument{}, err
	}
	if len(data) > MaxStatusDocumentSize {
		return StatusDocument{}, fmt.Errorf("status document exceeds %d bytes", MaxStatusDocumentSize)
	}
	var document StatusDocument
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return StatusDocument{}, fmt.Errorf("decode status document: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return StatusDocument{}, errors.New("status file must contain exactly one JSON object")
	}
	if err := document.Validate(); err != nil {
		return StatusDocument{}, err
	}
	return document, nil
}

func (d StatusDocument) Validate() error {
	if d.Version != StatusVersion {
		return fmt.Errorf("unsupported status version %d", d.Version)
	}
	if d.ProcessID <= 0 {
		return errors.New("status process_id must be positive")
	}
	if _, err := d.UpdatedTime(); err != nil {
		return err
	}
	for index, mapping := range d.Mappings {
		if err := validateMappingStatus(mapping); err != nil {
			return fmt.Errorf("mapping %d: %w", index, err)
		}
	}
	return nil
}

func (d StatusDocument) UpdatedTime() (time.Time, error) {
	updated, err := time.Parse(time.RFC3339Nano, d.UpdatedAt)
	if err != nil {
		return time.Time{}, fmt.Errorf("status updated_at is not RFC3339: %w", err)
	}
	return updated, nil
}

// ValidateFresh rejects snapshots older than maxAge and timestamps too far in
// the future. Callers should additionally check ProcessID liveness.
func (d StatusDocument) ValidateFresh(now time.Time, maxAge time.Duration) error {
	if maxAge <= 0 {
		return errors.New("status maximum age must be positive")
	}
	updated, err := d.UpdatedTime()
	if err != nil {
		return err
	}
	age := now.Sub(updated)
	if age > maxAge {
		return fmt.Errorf("status is stale by %s (maximum %s)", age.Round(time.Second), maxAge)
	}
	if age < -StatusHeartbeatInterval {
		return fmt.Errorf("status timestamp is %s in the future", (-age).Round(time.Second))
	}
	return nil
}

func validateMappingStatus(mapping MappingStatus) error {
	switch mapping.Network {
	case "tcp4", "tcp6", "udp4", "udp6":
	default:
		return fmt.Errorf("unsupported network %q", mapping.Network)
	}
	if err := validateStatusAddress(mapping.WindowsAddress, mapping.State == "rejected"); err != nil {
		return fmt.Errorf("invalid Windows address: %w", err)
	}
	if err := validateStatusAddress(mapping.WSLAddress, false); err != nil {
		return fmt.Errorf("invalid WSL address: %w", err)
	}
	switch mapping.State {
	case "active":
		if mapping.Error != "" || mapping.RetryAt != "" {
			return errors.New("active mapping cannot contain error or retry_at")
		}
	case "rejected":
		if mapping.Error == "" {
			return errors.New("rejected mapping must contain an error")
		}
		if len(mapping.Error) > maxStatusError {
			return fmt.Errorf("mapping error exceeds %d bytes", maxStatusError)
		}
		if mapping.RetryAt != "" {
			if _, err := time.Parse(time.RFC3339Nano, mapping.RetryAt); err != nil {
				return fmt.Errorf("retry_at is not RFC3339: %w", err)
			}
		}
	default:
		return fmt.Errorf("unsupported state %q", mapping.State)
	}
	return nil
}

func validateStatusAddress(address string, allowZero bool) error {
	_, portText, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 0 || port > 65535 || (!allowZero && port == 0) {
		return fmt.Errorf("invalid port %q", portText)
	}
	return nil
}
