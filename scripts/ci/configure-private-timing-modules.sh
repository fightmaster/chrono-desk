#!/usr/bin/env bash

set -euo pipefail

modules_read_token="${TIMING_MODULES_READ_TOKEN:-}"

if [[ -z "${modules_read_token}" ]]; then
    echo "::error title=Private module credential missing::TIMING_MODULES_READ_TOKEN is not visible to this GitHub Actions job. Add it as a repository Actions secret, not an environment secret or GitLab CI variable." >&2
    exit 1
fi

git config --global \
    url."https://oauth2:${modules_read_token}@gitlab.com/".insteadOf \
    "https://gitlab.com/"

for module in \
    gitlab.com/fightmaster1/timing-core \
    gitlab.com/fightmaster1/rfid-core
do
    if ! GOPRIVATE=gitlab.com/fightmaster1 \
        GONOSUMDB=gitlab.com/fightmaster1 \
        GIT_TERMINAL_PROMPT=0 \
        go mod download "${module}" >/dev/null 2>&1
    then
        echo "::error title=Private module access denied::Cannot download ${module}. Use a GitLab legacy PAT with read_repository or a fine-grained PAT with Code: Download across both projects. Fine-grained Code: Read and read_registry are insufficient." >&2
        exit 1
    fi
done
