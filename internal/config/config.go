package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

type File struct {
	RelayExecutable       string            `json:"relay_exe"`
	BrokerMode            bool              `json:"broker_mode"`
	UpstreamProxy         string            `json:"upstream_proxy"`
	SOCKS5Listen          string            `json:"socks5_listen"`
	HTTPConnectListen     string            `json:"http_connect_listen"`
	HTTPProxyListen       string            `json:"http_proxy_listen"`
	ControlSocket         string            `json:"control_socket"`
	StrictListenHost      string            `json:"strict_listen_host"`
	StrictListenHost6     string            `json:"strict_listen_host6"`
	RelayHandshakeTimeout string            `json:"relay_handshake_timeout"`
	RelayDialTimeout      string            `json:"relay_dial_timeout"`
	UDPAssociateIdle      string            `json:"udp_associate_idle_timeout"`
	ProxyHandshakeTimeout string            `json:"proxy_handshake_timeout"`
	Reverse               []string          `json:"reverse"`
	ReverseUDP            []string          `json:"reverse_udp"`
	AutoForward           AutoForwardConfig `json:"auto_forward"`
}

type AutoForwardConfig struct {
	Enabled           bool     `json:"enabled"`
	UDPEnabled        bool     `json:"udp_enabled"`
	WindowsHost       string   `json:"windows_host"`
	WindowsHost6      string   `json:"windows_host6"`
	WindowsPortOffset int      `json:"windows_port_offset"`
	Interval          string   `json:"interval"`
	RetryMin          string   `json:"retry_min"`
	RetryMax          string   `json:"retry_max"`
	Include           []uint16 `json:"include"`
	UDPInclude        []uint16 `json:"udp_include"`
	Exclude           []uint16 `json:"exclude"`
}

func Default() File {
	return File{
		RelayExecutable:       "wsl-win-relay.exe",
		SOCKS5Listen:          "127.0.0.1:1080",
		ControlSocket:         "/tmp/wsl-win-relay-control.sock",
		StrictListenHost:      "127.0.0.1",
		StrictListenHost6:     "::1",
		RelayHandshakeTimeout: "5s",
		RelayDialTimeout:      "30s",
		UDPAssociateIdle:      "5m",
		ProxyHandshakeTimeout: "15s",
		AutoForward:           AutoForwardConfig{WindowsHost: "127.0.0.1", WindowsHost6: "::1", Interval: "1s", RetryMin: "1s", RetryMax: "30s"},
	}
}

func Load(path string) (File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return File{}, err
	}
	result := Default()
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return File{}, fmt.Errorf("decode configuration: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return File{}, errors.New("configuration must contain exactly one JSON object")
	}
	if err := normalizeHTTPProxyListen(data, &result); err != nil {
		return File{}, err
	}
	if _, err := result.AutoForwardDuration(); err != nil {
		return File{}, err
	}
	if _, _, err := result.AutoForwardRetryDurations(); err != nil {
		return File{}, err
	}
	if err := validateAutoForwardPortOffset(result.AutoForward.WindowsPortOffset); err != nil {
		return File{}, err
	}
	if _, err := result.UDPAssociateIdleDuration(); err != nil {
		return File{}, err
	}
	if _, err := result.ProxyHandshakeDuration(); err != nil {
		return File{}, err
	}
	if _, err := result.RelayDialDuration(); err != nil {
		return File{}, err
	}
	if _, err := result.RelayHandshakeDuration(); err != nil {
		return File{}, err
	}
	if err := validatePortLists(result.AutoForward); err != nil {
		return File{}, err
	}
	return result, nil
}

func validateAutoForwardPortOffset(offset int) error {
	if offset < -65534 || offset > 65534 {
		return fmt.Errorf("auto_forward.windows_port_offset must be between -65534 and 65534")
	}
	return nil
}

func normalizeHTTPProxyListen(data []byte, result *File) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return fmt.Errorf("decode configuration fields: %w", err)
	}
	oldValue, oldSet := "", false
	newValue, newSet := "", false
	if raw, ok := fields["http_connect_listen"]; ok {
		oldSet = true
		if err := json.Unmarshal(raw, &oldValue); err != nil {
			return fmt.Errorf("http_connect_listen must be a string: %w", err)
		}
	}
	if raw, ok := fields["http_proxy_listen"]; ok {
		newSet = true
		if err := json.Unmarshal(raw, &newValue); err != nil {
			return fmt.Errorf("http_proxy_listen must be a string: %w", err)
		}
	}
	if oldSet && newSet && oldValue != newValue {
		return errors.New("http_connect_listen and http_proxy_listen must match when both are set")
	}
	switch {
	case newSet:
		result.HTTPProxyListen = newValue
		result.HTTPConnectListen = newValue
	case oldSet:
		result.HTTPProxyListen = oldValue
	default:
		result.HTTPProxyListen = result.HTTPConnectListen
	}
	return nil
}

func validatePortLists(config AutoForwardConfig) error {
	for name, ports := range map[string][]uint16{
		"auto_forward.include":     config.Include,
		"auto_forward.udp_include": config.UDPInclude,
		"auto_forward.exclude":     config.Exclude,
	} {
		for _, port := range ports {
			if port == 0 {
				return fmt.Errorf("%s must contain ports between 1 and 65535", name)
			}
		}
	}
	return nil
}

func (f File) AutoForwardDuration() (time.Duration, error) {
	duration, err := time.ParseDuration(f.AutoForward.Interval)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("auto_forward.interval must be a positive duration")
	}
	return duration, nil
}

func (f File) AutoForwardRetryDurations() (time.Duration, time.Duration, error) {
	minimum, err := time.ParseDuration(f.AutoForward.RetryMin)
	if err != nil || minimum <= 0 {
		return 0, 0, fmt.Errorf("auto_forward.retry_min must be a positive duration")
	}
	maximum, err := time.ParseDuration(f.AutoForward.RetryMax)
	if err != nil || maximum <= 0 {
		return 0, 0, fmt.Errorf("auto_forward.retry_max must be a positive duration")
	}
	if maximum < minimum {
		return 0, 0, fmt.Errorf("auto_forward.retry_max must be greater than or equal to auto_forward.retry_min")
	}
	return minimum, maximum, nil
}

func (f File) UDPAssociateIdleDuration() (time.Duration, error) {
	duration, err := time.ParseDuration(f.UDPAssociateIdle)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("udp_associate_idle_timeout must be a positive duration")
	}
	return duration, nil
}

func (f File) ProxyHandshakeDuration() (time.Duration, error) {
	duration, err := time.ParseDuration(f.ProxyHandshakeTimeout)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("proxy_handshake_timeout must be a positive duration")
	}
	return duration, nil
}

func (f File) RelayDialDuration() (time.Duration, error) {
	duration, err := time.ParseDuration(f.RelayDialTimeout)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("relay_dial_timeout must be a positive duration")
	}
	return duration, nil
}

func (f File) RelayHandshakeDuration() (time.Duration, error) {
	duration, err := time.ParseDuration(f.RelayHandshakeTimeout)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("relay_handshake_timeout must be a positive duration")
	}
	return duration, nil
}
