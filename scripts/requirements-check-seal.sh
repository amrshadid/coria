#!/bin/bash
# Prints the hashed requirements behind scripts/requirements-check-seal.txt. The
# packages are the "name==version" lines of that file; to change a version,
# change it there and run this again. It is "scripts/requirements.sh check-seal",
# kept under this name for whoever runs it so.
#
#   scripts/requirements-check-seal.sh [python]
set -euo pipefail
exec bash "$(dirname "$0")/requirements.sh" check-seal "$@"
