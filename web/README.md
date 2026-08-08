# Giao diện Collectr

```bash
npm install
npm run dev     # Vite ở :5173, proxy API sang Go ở :8080
npm run test    # engine.ts chấm bằng fixture của Go
npm run build   # ghi thẳng vào ../internal/webui/dist để go:embed nhúng
```

Chạy `npm run build` rồi `go build ./cmd/collectr` là có một binary tự phục vụ
giao diện. `docker compose up` làm sẵn cả hai bước.
