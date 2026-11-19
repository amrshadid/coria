# Sourced by scripts/prove/run.sh, in the order it gives: the checks of
# section "11. the static builds (made first: the binary under test is one of them)".
# It uses the runner's variables and helpers (result, ok, fail, skip, api,
# cli, the work directory) and sets what later sections read.
# shellcheck shell=bash

section "11. the static builds (made first: the binary under test is one of them)"
host_os=$("$GO" env GOOS)
host_arch=$("$GO" env GOARCH)
mkdir -p "$work/bin"
if [ "$BUILD" = host ]; then
	CGO_ENABLED=0 "$GO" build -trimpath -ldflags "-s -w" -o "$work/bin/magnetar-$host_os-$host_arch" "$root/cmd/magnetar"
else
	if ! make -C "$root" --no-print-directory build BIN="$work/bin" GO="$GO" > "$work/build.log" 2>&1; then
		fail "11.1 a static build for every target of make build" "make build failed" "$(tail -20 "$work/build.log")"
		exit 1
	fi
fi
bin=$work/bin/magnetar-$host_os-$host_arch
[ -x "$bin" ] || { fail "11.1 a static build for every target of make build" "no binary for $host_os/$host_arch in $work/bin"; exit 1; }
echo "built $(ls "$work/bin" | tr '\n' ' ')"
