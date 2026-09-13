#!/bin/sh
set -eu

repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
run_go=1
windows_interop=0

usage() {
    cat <<'EOF'
Usage: ./scripts/test-release.sh [--native-only] [--windows-interop]

Runs the complete offline release gate in WSL. --native-only skips Go
test/vet/race for CI jobs that already ran those checks. --windows-interop
adds tests that launch the built Windows executables through WSL interop.
EOF
}

while [ "$#" -gt 0 ]; do
    case "$1" in
        --native-only) run_go=0 ;;
        --windows-interop) windows_interop=1 ;;
        -h|--help) usage; exit 0 ;;
        *) echo "unknown release-test option: $1" >&2; usage >&2; exit 2 ;;
    esac
    shift
done

case "$(uname -m)" in
    x86_64|amd64) ;;
    *)
        echo "release runtime verification requires an amd64 WSL/Linux host; aarch64 is build-only" >&2
        exit 1
        ;;
esac

run() {
    label=$1
    shift
    printf '\n==> %s\n' "$label"
    "$@"
}

export GOTOOLCHAIN=local
export GOPROXY=off

cd "$repo_dir"
if [ "$run_go" -eq 1 ]; then
    run "Go tests" go test ./...
    run "Go vet" go vet ./...
    run "Go race detector" go test -race ./...
fi

run "Shell syntax" sh -n scripts/*.sh scripts/wsl-win-relay-*
run "amd64 Linux and Windows build" "$repo_dir/scripts/build-wsl.sh"
run "Build metadata" "$repo_dir/scripts/test-build-metadata.sh"
run "User-service installer" "$repo_dir/scripts/test-user-installer.sh"
run "Doctor" "$repo_dir/scripts/test-doctor.sh"
run "Strict launcher preflight" "$repo_dir/scripts/test-launcher-preflight.sh"
run "Kernel strict supervisor" "$repo_dir/scripts/test-kernel-supervisor.sh"
run "LD_PRELOAD interposer" "$repo_dir/scripts/test-interposer.sh"
run "Transparent routing rollback" "$repo_dir/scripts/test-transparent-relay.sh"
run "Transparent-service installer" "$repo_dir/scripts/test-transparent-installer.sh"
run "Broker connector" "$repo_dir/scripts/test-broker-connector.sh"
run "Broker connector recovery" "$repo_dir/scripts/test-broker-reconnect.sh"
run "Broker automatic mapping recovery" "$repo_dir/scripts/test-broker-auto-rebind.sh"
run "Broker process recovery" "$repo_dir/scripts/test-broker-restart.sh"
run "Broker host supervisor" "$repo_dir/scripts/test-broker-supervisor.sh"
run "Broker service wrapper" "$repo_dir/scripts/test-broker-service-wrapper.sh"
run "Broker-service installer" "$repo_dir/scripts/test-broker-installer.sh"

if [ "$windows_interop" -eq 1 ]; then
    run "Windows stdio automatic mapping recovery" "$repo_dir/scripts/test-auto-rebind.sh"
    run "Windows broker end-to-end interop" "$repo_dir/scripts/test-broker-windows-interop.sh"
fi

suffix=
if [ "$windows_interop" -eq 1 ]; then
    suffix=" with Windows interop"
fi
printf '\nRelease verification passed%s.\n' "$suffix"
