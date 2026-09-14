#!/bin/sh
# Cross-build all targets with CGO disabled.
set -e
cd "$(dirname "$0")"
exec make cross "$@"
