#!/usr/bin/env bash
set -euo pipefail

# Wails disables Go's source metadata by default. Preserve its commands and
# argument boundaries, but stamp actual source identity in release binaries.
arguments=()
for argument in "$@"; do
    if [[ "$argument" == '-buildvcs=false' ]]; then
        arguments+=('-buildvcs=true')
    else
        arguments+=("$argument")
    fi
done
exec go "${arguments[@]}"
