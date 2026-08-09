#!/usr/bin/env bash
#
# Sao lưu Collectr.
#
# Sinh ra HAI tệp, và cần cả hai để khôi phục được một hệ thống dùng được:
#
#   collectr-<ngày>.dump      cơ sở dữ liệu
#   collectr-<ngày>.kek       TENANT_KEK trích từ .env
#
# Chỉ có bản dump thì khôi phục ra được các bảng, đăng nhập được, xem được lưới
# — nhưng mọi field nhạy cảm và mọi tệp đính kèm là byte không đọc nổi, vĩnh
# viễn. Khoá không nằm trong cơ sở dữ liệu; đó chính là thuộc tính khiến xoá
# triệt để có hiệu lực, và nó cắt cả hai chiều.
#
# CẤT HAI TỆP NÀY Ở HAI NƠI KHÁC NHAU. Để chung một thư mục thì lớp mã hoá chỉ
# còn là thủ tục: ai lấy được bản sao lưu là lấy được cả khoá mở nó.
#
#   ./deploy/backup.sh [thư-mục-đích]
#
set -euo pipefail

DEST="${1:-./backups}"
STAMP="$(date +%Y%m%d-%H%M%S)"
DUMP="$DEST/collectr-$STAMP.dump"
KEK="$DEST/collectr-$STAMP.kek"

mkdir -p "$DEST"

echo "→ dump cơ sở dữ liệu"
# --format=custom: nén sẵn, và cho phép pg_restore chạy song song lúc khôi phục.
# --clean --if-exists nằm ở phía restore, không phải ở đây: một bản dump mang
# sẵn lệnh DROP là một bản sao lưu có thể xoá dữ liệu nếu chạy nhầm chỗ.
docker compose exec -T postgres pg_dump \
    --username=collectr --dbname=collectr \
    --format=custom --compress=6 --no-owner \
    > "$DUMP"

echo "→ trích TENANT_KEK"
if ! grep -q '^TENANT_KEK=' .env; then
    echo "   .env không có TENANT_KEK — dừng lại." >&2
    echo "   Sao lưu chỉ có dump là bản sao lưu không khôi phục được dữ liệu nhạy cảm." >&2
    rm -f "$DUMP"
    exit 1
fi
grep '^TENANT_KEK=' .env > "$KEK"
chmod 600 "$KEK"

# Kiểm ngay tại chỗ rằng bản dump đọc được. Một tệp 0 byte hay bị cắt ngang vẫn
# nằm im trong thư mục sao lưu hàng tháng trời mà không ai biết, cho tới đúng
# ngày cần tới nó.
echo "→ kiểm tra bản dump đọc được"
docker compose exec -T postgres pg_restore --list < "$DUMP" > /dev/null

echo
echo "   $DUMP  ($(du -h "$DUMP" | cut -f1))"
echo "   $KEK   (chmod 600 — cất TÁCH KHỎI bản dump)"
echo
echo "Bản sao lưu chưa từng khôi phục thử không phải là bản sao lưu."
echo "Diễn tập: ./deploy/restore.sh $DUMP"
