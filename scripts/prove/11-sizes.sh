# Sourced by scripts/prove/run.sh, in the order it gives: the checks of
# section "11. the static builds and their sizes".
# It uses the runner's variables and helpers (result, ok, fail, skip, api,
# cli, the work directory) and sets what later sections read.
# shellcheck shell=bash

section "11. the static builds and their sizes"
if [ "$BUILD" = host ]; then
	skip "11.1 a static build for every target of make build" "MAGNETAR_PROVE_BUILD=host: only $host_os/$host_arch was built"
else
	targets=$(sed -n 's/^PLATFORMS *:= *//p' "$root/Makefile")
	bad=""
	{
		echo "| Binary | Bytes | MiB |"
		echo "|---|---:|---:|"
	} > "$work/sizes.txt"
	for t in $targets; do
		f=$work/bin/magnetar-${t%/*}-${t#*/}
		[ "${t%/*}" = windows ] && f=$f.exe
		if [ ! -f "$f" ]; then
			bad="$bad $t: missing;"
			continue
		fi
		bytes=$(wc -c < "$f" | tr -d ' ')
		echo "| $(basename "$f") | $bytes | $(awk -v b="$bytes" 'BEGIN { printf "%.2f", b / 1048576 }') |" >> "$work/sizes.txt"
		"$GO" version -m "$f" | grep -q "CGO_ENABLED=0" || bad="$bad $t: not built with CGO_ENABLED=0;"
		if [ "${t%/*}" = linux ] && command -v file > /dev/null; then
			file "$f" | grep -q 'statically linked' || bad="$bad $t: not statically linked ($(file -b "$f" | cut -c 1-60));"
		fi
	done
	if [ -z "$bad" ]; then
		ok "11.1 a static build for every target of make build" "$(echo "$targets" | wc -w | tr -d ' ') targets ($targets), each CGO_ENABLED=0 (go version -m), the Linux ones statically linked (file); sizes below"
	else
		fail "11.1 a static build for every target of make build" "$bad"
	fi
fi
