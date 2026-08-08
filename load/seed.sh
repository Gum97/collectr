#!/usr/bin/env bash
#
# Seeds what the k6 scripts need, and prints the environment they read.
#
# Written after having to reconstruct it: the scripts expect link codes
# load0001..loadNNNN, one expired code `loadgone`, and a form whose schema has
# the exact field ids submit.js posts. None of that was written down, so the
# numbers in docs/02-estimation.md could not be reproduced without guessing --
# which is the same as not having them.
#
#   ./load/seed.sh                    # against http://localhost
#   eval "$(./load/seed.sh --quiet)"  # export the variables into this shell
#
set -euo pipefail

BASE="${BASE:-http://localhost}"
EMAIL="${EMAIL:?set EMAIL to an owner account}"
PASSWORD="${PASSWORD:?set PASSWORD}"
LINKS="${LINKS:-500}"
JAR="$(mktemp)"
QUIET=""
[ "${1:-}" = "--quiet" ] && QUIET=1

say() { [ -n "$QUIET" ] || echo "$@" >&2; }
api() { curl -sS -b "$JAR" -H 'Content-Type: application/json' -H "Origin: $BASE" "$@"; }

say "→ đăng nhập $EMAIL"
printf '{"email":"%s","password":"%s"}' "$EMAIL" "$PASSWORD" \
  | curl -sS -c "$JAR" -X POST "$BASE/api/auth/login" \
      -H 'Content-Type: application/json' -H "Origin: $BASE" --data-binary @- -o /dev/null

PROJECT=$(api "$BASE/api/v1/projects" | python3 -c 'import json,sys; print(json.load(sys.stdin)["data"][0]["id"])')
say "→ dự án $PROJECT"

say "→ $LINKS link (load0001..)"
for i in $(seq 1 "$LINKS"); do
  code=$(printf "load%04d" "$i")
  printf '{"project_id":"%s","target_url":"https://example.com/load/%d","alias":"%s"}' \
    "$PROJECT" "$i" "$code" \
    | api -X POST "$BASE/api/v1/links" --data-binary @- -o /dev/null || true
done

# redirect.js asks for one code that is gone, because the negative path is the
# one the cache design is actually about.
printf '{"project_id":"%s","target_url":"https://example.com/gone","alias":"loadgone"}' "$PROJECT" \
  | api -X POST "$BASE/api/v1/links" --data-binary @- -o /dev/null || true
say "  (đặt expires_at trong quá khứ cho loadgone bằng SQL, rồi FLUSHDB redis)"

say "→ mục đích xử lý"
for p in '{"code":"service","name":"Cung cap dich vu","legal_basis":"contract"}' \
         '{"code":"marketing","name":"Gui thong tin khuyen mai","legal_basis":"consent"}'; do
  printf '%s' "$p" | api -X POST "$BASE/api/v1/consent/purposes" --data-binary @- -o /dev/null || true
done

# The form's public payload only carries a consent block when a consent_text
# document is active; without one every submission is refused for missing proof.
say "→ văn bản đồng ý (kind=consent_text)"
printf '{"kind":"consent_text","body_html":"<h2>Dong y xu ly du lieu</h2><p>Chung toi thu thap ho ten va so dien thoai de xu ly yeu cau cua ban.</p>"}' \
  | api -X POST "$BASE/api/v1/consent/documents" --data-binary @- -o /dev/null || true

say "→ biểu mẫu khớp đúng field id mà submit.js gửi"
python3 - "$PROJECT" > /tmp/collectr-load-form.json <<'PY'
import json, sys
schema = {
    "v": 1,
    "pages": [{"id": "pg_1", "title": "Trang 1",
               "fields": ["f_name", "f_phone", "f_used", "f_rating", "f_health"]}],
    "fields": {
        "f_name":   {"type": "text", "label": "Ho va ten", "required": True, "pii": "name"},
        "f_phone":  {"type": "text", "label": "So dien thoai", "required": True,
                     "format": "phone_vn", "pii": "phone", "identifier": True},
        "f_used":   {"type": "choice", "label": "Ban da dung dich vu chua?", "required": True,
                     "options": [{"id": "o_yes", "label": "Roi"}, {"id": "o_no", "label": "Chua"}]},
        "f_rating": {"type": "rating", "label": "Muc do hai long", "scale": 5, "hidden": True},
        "f_health": {"type": "text", "label": "Tinh trang suc khoe", "hidden": True,
                     "sensitive": True, "pii": "health"},
    },
    # Half the traffic walks the branch, so half of it writes an encrypted
    # answer. A load test without that measures the cheap path only.
    "rules": [{"id": "r_1", "on_page": "pg_1",
               "when": {"op": "eq", "field": "f_used", "value": "o_yes"},
               "then": [{"action": "show", "target": "f_rating"},
                        {"action": "show", "target": "f_health"}]}],
    "consent": {"purposes": [{"code": "service", "required": True}, {"code": "marketing"}],
                "sensitive_notice_required": True},
}
print(json.dumps({"project_id": sys.argv[1], "title": "Load test form", "draft": schema}))
PY

FORM=$(api -X POST "$BASE/api/v1/forms" --data-binary @/tmp/collectr-load-form.json)
FORM_ID=$(printf '%s' "$FORM" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
PUBLIC_ID=$(printf '%s' "$FORM" | python3 -c 'import json,sys; print(json.load(sys.stdin)["public_id"])')
api -X POST "$BASE/api/v1/forms/$FORM_ID/draft/publish" -o /dev/null

curl -sS "$BASE/api/pub/forms/$PUBLIC_ID" | python3 -c '
import json, sys
d = json.load(sys.stdin)
c = d.get("consent") or {}
print("export LOAD_FORM=%s"          % d["form"]["public_id"])
print("export LOAD_FORM_VERSION=%s"  % d["version"]["id"])
print("export LOAD_CONSENT_DOC=%s"   % c.get("document_id"))
print("export LOAD_CONSENT_HASH=%s"  % (c.get("content_hash") or "").replace("sha256:", ""))
'
say ""
say "Chạy: make load    (nâng PUBLIC_WRITE_IP_LIMIT nếu muốn đo năng lực ứng dụng"
say "                    thay vì đo chính cái rate limit — xem docs/02-estimation.md §2.6)"
rm -f "$JAR"
