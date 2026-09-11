#!/bin/sh
set -eu

# Pin the integration to a released upstream version for reproducible setups.
version=${TUN2SOCKS_VERSION:-v2.7.0}
GOTOOLCHAIN=local go install "github.com/xjasonlyu/tun2socks/v2@$version"
echo "installed tun2socks $version to $(go env GOPATH)/bin/tun2socks"
