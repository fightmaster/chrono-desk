#!/usr/bin/env bash
set -euo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
test_dir=$(mktemp -d)
trap 'rm -rf "$test_dir"' EXIT
mkdir "$test_dir/bin"
cat > "$test_dir/bin/go" <<'MOCK'
#!/usr/bin/env bash
printf '%s\n' "$@" > "$MOCK_GO_ARGUMENTS"
MOCK
chmod +x "$test_dir/bin/go"
export PATH="$test_dir/bin:$PATH"
export MOCK_GO_ARGUMENTS="$test_dir/actual"

bash "$script_dir/go-source-build.sh" build '-buildvcs=false' -ldflags 'value with spaces' .
printf '%s\n' build '-buildvcs=true' -ldflags 'value with spaces' . > "$test_dir/expected"
diff -u "$test_dir/expected" "$test_dir/actual"

bash "$script_dir/go-source-build.sh" mod tidy
printf '%s\n' mod tidy > "$test_dir/expected"
diff -u "$test_dir/expected" "$test_dir/actual"

bash "$script_dir/go-source-build.sh" build '-buildvcs=true' .
printf '%s\n' build '-buildvcs=true' . > "$test_dir/expected"
diff -u "$test_dir/expected" "$test_dir/actual"
printf 'Source metadata compiler wrapper checks passed\n'
