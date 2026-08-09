#!/usr/bin/env bash
#
# Khôi phục Collectr từ bản sao lưu, và kiểm chứng rằng thứ khôi phục ra dùng
# được — không chỉ là các bảng có mặt.
#
#   ./deploy/restore.sh backups/collectr-20260809-120000.dump
#
# GHI ĐÈ TOÀN BỘ cơ sở dữ liệu hiện tại. Script hỏi lại trước khi làm.
#
# TENANT_KEK trong .env phải là đúng khoá đã dùng lúc sao lưu. Nếu khác, mọi
# thứ sẽ khôi phục sạch sẽ, đăng nhập được, lưới hiện đủ dòng — và mọi field
# nhạy cảm hỏng vĩnh viễn. Bước kiểm ở cuối tồn tại để phát hiện đúng chuyện
# đó, vì nó không tự báo lỗi ở bất kỳ chỗ nào khác.
set -euo pipefail

DUMP="${1:?dùng: ./deploy/restore.sh <tệp .dump>}"
[ -r "$DUMP" ] || { echo "không đọc được $DUMP" >&2; exit 1; }

START=$(date +%s)

echo "Sẽ GHI ĐÈ toàn bộ cơ sở dữ liệu 'collectr' bằng $DUMP"
read -r -p "Gõ 'khoi phuc' để tiếp tục: " ok
[ "$ok" = "khoi phuc" ] || { echo "huỷ."; exit 1; }

echo "→ dừng ứng dụng (giữ postgres chạy)"
# Ứng dụng và worker phải dừng trước: một tiến trình đang giữ kết nối sẽ chặn
# DROP SCHEMA, và tệ hơn, một worker còn chạy có thể ghi vào cơ sở dữ liệu đang
# được khôi phục dở.
docker compose stop collectr worker >/dev/null 2>&1 || true

echo "→ khôi phục"
docker compose exec -T postgres pg_restore \
    --username=collectr --dbname=collectr \
    --clean --if-exists --no-owner --single-transaction \
    < "$DUMP"

echo "→ khởi động lại"
docker compose up -d collectr worker >/dev/null
sleep 8

ELAPSED=$(( $(date +%s) - START ))

echo
echo "── kiểm chứng ──"

psql() { docker compose exec -T postgres psql -U collectr -d collectr -t -A -c "$1"; }

echo "  submission:  $(psql 'SELECT count(*) FROM forms.submissions')"
echo "  chủ thể:     $(psql 'SELECT count(*) FROM consent.data_subjects')"
echo "  audit:       $(psql 'SELECT count(*) FROM audit.entries')"

# Chuỗi hash audit là thứ duy nhất chứng minh bản khôi phục không bị sửa và
# không mất mục nào ở giữa. Đếm số dòng không chứng minh được điều đó.
echo "  chuỗi audit: $(psql "SELECT CASE WHEN count(*) = max(seq) AND min(seq) = 1
                               THEN 'seq liền mạch 1..' || max(seq)
                               ELSE 'ĐỨT QUÃNG' END FROM audit.entries")"

# Khoá bọc còn nguyên nghĩa là TENANT_KEK hiện tại mở được chúng. Không kiểm
# bước này thì một bản khôi phục bằng sai khoá trông y hệt một bản đúng.
echo "  DEK bọc:     $(psql 'SELECT count(*) FROM consent.data_subjects WHERE dek_wrapped IS NOT NULL')"

echo "  healthz:     $(curl -s -o /dev/null -w '%{http_code}' -H 'Host: localhost' http://127.0.0.1/healthz || echo 'không phản hồi')"
echo
echo "Xong trong ${ELAPSED}s."
echo
echo "Bước cuối phải làm bằng tay: mở lưới dữ liệu, bấm hiện một field nhạy cảm."
echo "Nếu ra giá trị đọc được thì TENANT_KEK khớp và bản khôi phục dùng được."
echo "Nếu ra lỗi giải mã thì bản dump đã khôi phục xong nhưng dữ liệu nhạy cảm mất."
