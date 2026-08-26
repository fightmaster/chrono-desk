#!/usr/bin/env bash

set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
subject="${script_dir}/configure-private-timing-modules.sh"
test_root="$(mktemp -d)"
trap 'rm -rf -- "${test_root}"' EXIT

mkdir -p "${test_root}/bin"

cat > "${test_root}/bin/git" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF

cat > "${test_root}/bin/go" <<'EOF'
#!/usr/bin/env bash
if [[ "${FAIL_MODULE:-}" == "${3:-}" ]]; then
    exit 1
fi
exit 0
EOF

chmod +x "${test_root}/bin/git" "${test_root}/bin/go"

run_subject() {
    PATH="${test_root}/bin:${PATH}" bash "${subject}" 2>&1
}

assert_fails_with() {
    local expected="$1"
    shift
    local output

    if output="$("$@" 2>&1)"; then
        echo "expected command to fail" >&2
        exit 1
    fi

    if [[ "${output}" != *"${expected}"* ]]; then
        printf 'expected output to contain: %s\nactual output: %s\n' \
            "${expected}" "${output}" >&2
        exit 1
    fi
}

assert_fails_with \
    "TIMING_MODULES_READ_TOKEN is not visible" \
    env -u TIMING_MODULES_READ_TOKEN -u TIMING_CORE_READ_TOKEN \
    PATH="${test_root}/bin:${PATH}" bash "${subject}"

assert_fails_with \
    "Cannot download gitlab.com/fightmaster1/timing-core" \
    env TIMING_MODULES_READ_TOKEN=test-token \
    FAIL_MODULE=gitlab.com/fightmaster1/timing-core \
    PATH="${test_root}/bin:${PATH}" bash "${subject}"

assert_fails_with \
    "Cannot download gitlab.com/fightmaster1/rfid-core" \
    env TIMING_MODULES_READ_TOKEN=test-token \
    FAIL_MODULE=gitlab.com/fightmaster1/rfid-core \
    PATH="${test_root}/bin:${PATH}" bash "${subject}"

TIMING_MODULES_READ_TOKEN=test-token run_subject >/dev/null

echo "private timing module preflight tests passed"
