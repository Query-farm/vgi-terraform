#!/usr/bin/env bash
# Regenerate the Terraform/OpenTofu state and plan fixtures in internal/terraform/samples/ (served as sample:<name>).
#
#   scripts/gen-terraform-fixtures.sh /path/to/tofu   (or terraform)
#
# Stage 1 is applied (null/random/local providers only: no cloud account), its
# state is saved, then stage 2's configuration is planned against that state:
# an in-place update, a forced replacement, a create, a destroy and no-ops.
set -euo pipefail
TF=${1:-tofu}
ROOT=$(cd "$(dirname "$0")/.." && pwd)
SRC=$ROOT/test/fixtures/tf
OUT=$ROOT/internal/terraform/samples
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

cp -R "$SRC/stage1/." "$WORK/"
(cd "$WORK" && "$TF" init -input=false -no-color >/dev/null && "$TF" apply -auto-approve -input=false -no-color >/dev/null)
cp "$WORK/terraform.tfstate" "$OUT/basic.tfstate"

# Stage 2 keeps the state and lock file and swaps the configuration.
rm -f "$WORK/main.tf"
cp "$SRC/stage2/main.tf" "$WORK/main.tf"
(cd "$WORK" && "$TF" plan -input=false -no-color -out=plan.bin >/dev/null && "$TF" show -json plan.bin > plan.json)
python3 -m json.tool "$WORK/plan.json" > "$OUT/change.plan.json"
echo "wrote $OUT/basic.tfstate and $OUT/change.plan.json"
