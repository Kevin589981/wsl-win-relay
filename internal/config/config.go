package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

type File struct {
	RelayExecutable   string            `json:"relay_exe"`
	SOCKS5Listen      string            `json:"socks5_listen"`
	HTTPConnectListen string            `json:"http_connect_listen"`
	ControlSocket     string            `json:"control_socket"`
	StrictListenHost  string            `json:"strict_listen_host"`
	UDPAssociateIdle  string            `json:"udp_associate_idle_timeout"`
	Reverse           []string          `json:"reverse"`
	AutoForward       AutoForwardConfig `json:"auto_forward"`
}

type AutoForwardConfig struct {
	Enabled     bool     `json:"enabled"`
	WindowsHost string   `json:"windows_host"`
	Interval    string   `json:"interval"`
	Include     []uint16 `json:"include"`
	Exclude     []uint16 `json:"exclude"`
}

func Default() File {
	return File{
		RelayExecutable:  "wsl-win-relay.exe",
		SOCKS5Listen:     "127.0.0.1:1080",
		ControlSocket:    "/tmp/wsl-win-relay-control.sock",
		StrictListenHost: "127.0.0.1",
		UDPAssociateIdle: "5m",
		AutoForward:      AutoForwardConfig{WindowsHost: "127.0.0.1", Interval: "1s"},
	}
}

func Load(path string) (File, error) {
	file, err := os.Open(path)
	if err != nil {
		return File{}, err
	}
	defer file.Close()
	result := Default()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return File{}, fmt.Errorf("decode configuration: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return File{}, errors.New("configuration must contain exactly one JSON object")
	}
	if _, err := result.AutoForwardDuration(); err != nil {
		return File{}, err
	}
	if _, err := result.UDPAssociateIdleDuration(); err != nil {
		return File{}, err
	}
	return result, nil
}

func (f File) AutoForwardDuration() (time.Duration, error) {
	duration, err := time.ParseDuration(f.AutoForward.Interval)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("auto_forward.interval must be a positive duration")
	}
	return duration, nil
}

func (f File) UDPAssociateIdleDuration() (time.Duration, error) {
	duration, err := time.ParseDuration(f.UDPAssociateIdle)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("udp_associate_idle_timeout must be a positive duration")
	}
	return duration, nil
}
