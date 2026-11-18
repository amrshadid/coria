#!/bin/bash
# Prints the files and SHA-256 hashes behind scripts/requirements-check-server.txt,
# as scripts/requirements-check-seal.sh does for the seal check: it downloads,
# with pip, the wheel pip would choose for each package on each platform the
# outside check of the server runs on (Linux x86_64 with CPython 3.12 to 3.14,
# as CI may have, and this machine), then hashes each file with "pip hash".
# Copy the hashes into the requirements file under their package.
#
#   scripts/requirements-check-server.sh [python]
set -euo pipefail
py="${1:-python3}"
packages=(grpcio==1.85.1 grpcio-tools==1.85.1 protobuf==7.36.2 prometheus-client==0.26.0 typing-extensions==4.16.0 setuptools==84.2.1)
dir="$(mktemp -d)"
trap 'rm -rf "$dir"' EXIT

for version in 3.12 3.13 3.14; do
	"$py" -m pip download --quiet --no-deps --only-binary=:all: \
		--platform manylinux_2_34_x86_64 --platform manylinux_2_28_x86_64 \
		--platform manylinux_2_17_x86_64 --platform manylinux2014_x86_64 \
		--python-version "$version" --implementation cp -d "$dir" "${packages[@]}"
done
"$py" -m pip download --quiet --no-deps --only-binary=:all: -d "$dir" "${packages[@]}"

for file in "$dir"/*; do
	echo "$(basename "$file") $("$py" -m pip hash "$file" | tail -1)"
done
