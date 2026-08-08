/**
 * 1o -- one link, read back.
 *
 * The whole screen is organised around a single fact the API is explicit about:
 * `clicks` comes from the rollups and covers the entire selected window, while
 * every breakdown below comes from raw events and reaches back only as far as
 * their retention allows. Those two totals are printed next to each other with
 * their coverage named, and no ratio here is ever taken across them -- a share
 * computed against the wrong denominator produced a negative rate on the server
 * once, and it would be silently wrong here rather than loudly wrong.
 */
import { useMemo, useState } from 'react'
import { Link as RouterLink, useNavigate, useParams } from 'react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, RequestFailed, type List } from '../../lib/api'
import { can, useMe } from '../../lib/session'
import {
  Card,
  Empty,
  ErrorBanner,
  Loading,
  PageHeader,
  StatusPill,
  Table,
  Td,
  Th,
  Tr,
  date,
  dateTime,
  num,
  pct,
} from '../../components/ui'
import {
  QRImage,
  bareShortURL,
  hostOfShortURL,
  linkState,
  useCopy,
  type FormOption,
  type LinkRow,
} from './Links'

/* ------------------------------------------------------------------ types */

interface ClickPoint {
  bucket: string
  clicks: number
}

/** One slice of a dimension. `networks` is a count of distinct /24 blocks, not
 *  of people -- see the label wherever it is rendered. */
interface Slice {
  key: string
  clicks: number
  networks: number
}

interface LinkStats {
  link: { id: string; code: string; short_url: string; status: string }
  from: string
  to: string
  /** From the rollups: covers the whole window. */
  clicks: number
  /** From raw events: the denominator for every ratio on this screen. */
  breakdown_clicks: number
  networks: number
  clicks_per_network: number
  qr_share: number
  first_click: string | null
  last_click: string | null
  points: ClickPoint[]
  sources: Slice[]
  referrers: Slice[]
  browsers: Slice[]
  utm_sources: Slice[]
  utm_mediums: Slice[]
  utm_campaigns: Slice[]
  breakdown_at: string
  breakdown_note: string
}

const RANGES = [
  { days: 7, label: '7 ngày', bucket: 'hour' },
  { days: 30, label: '30 ngày', bucket: 'day' },
  { days: 90, label: '90 ngày', bucket: 'day' },
  { days: 365, label: '1 năm', bucket: 'week' },
] as const

/* ---------------------------------------------------------------- screen */

export function LinkDetail() {
  const { projectId, linkId } = useParams()
  const me = useMe()
  const mayReadStats = can(me.data, 'analytics.read')
  const mayDelete = can(me.data, 'link.delete')
  const [rangeDays, setRangeDays] = useState<number>(30)
  const { copied, copy } = useCopy()

  const range = RANGES.find((r) => r.days === rangeDays) ?? RANGES[1]
  const period = useMemo(() => {
    const to = new Date()
    const from = new Date(to.getTime() - range.days * 86_400_000)
    return { from: from.toISOString(), to: to.toISOString(), bucket: range.bucket }
  }, [range])

  const link = useQuery({
    queryKey: ['link', linkId],
    queryFn: async () => api.get<LinkRow>(`/api/v1/links/${linkId}`),
    enabled: Boolean(linkId),
  })

  const forms = useQuery({
    queryKey: ['forms', projectId],
    queryFn: async () => (await api.get<List<FormOption>>(`/api/v1/forms?project_id=${projectId}`)).data,
    enabled: Boolean(projectId) && Boolean(link.data?.form_id),
  })

  const stats = useQuery({
    queryKey: ['link-stats', linkId, period.from, period.bucket],
    queryFn: async () =>
      api.get<LinkStats>(
        `/api/v1/links/${linkId}/stats?from=${encodeURIComponent(period.from)}&to=${encodeURIComponent(period.to)}&bucket=${period.bucket}`,
      ),
    enabled: Boolean(linkId) && mayReadStats,
  })

  const s = stats.data
  const l = link.data
  const formTitle = forms.data?.find((f) => f.id === l?.form_id)?.title

  return (
    <div className="p-6">
      <PageHeader
        title={l ? `/${l.code}` : 'Link'}
        meta={
          l ? (
            <>
              {bareShortURL(l.short_url)} · tạo {date(l.created_at)}
              {l.expires_at ? ` · hết hạn ${date(l.expires_at)}` : ' · không đặt hạn'}
            </>
          ) : (
            '…'
          )
        }
        actions={
          <>
            <RouterLink to={`/p/${projectId}/links`} className="btn">
              ← Danh sách
            </RouterLink>
            {l && (
              <button type="button" className="btn" onClick={() => copy(l.short_url)}>
                {copied === l.short_url ? 'Đã sao chép' : 'Sao chép link'}
              </button>
            )}
          </>
        }
      />

      {link.isPending && <Loading />}
      {link.isError && <ErrorBanner error={link.error} retry={() => void link.refetch()} />}

      {l && (
        <div className="grid gap-3">
          <div className="grid gap-3 md:grid-cols-[280px_1fr]">
            <QRPanel link={l} />
            <Card
              title="Link này"
              aside={<StatusPill tone={linkState(l).tone}>{linkState(l).label}</StatusPill>}
            >
              <dl className="grid grid-cols-[110px_1fr] gap-x-3 gap-y-1.5 text-body">
                <dt className="text-muted">URL rút gọn</dt>
                <dd className="font-mono text-meta">{l.short_url}</dd>
                <dt className="text-muted">Tên miền</dt>
                <dd>
                  {hostOfShortURL(l.short_url) || '—'}
                  <span className="id-chip ml-1">link giữ tên miền này suốt đời</span>
                </dd>
                <dt className="text-muted">Đích</dt>
                <dd className="break-all">
                  {l.form_id ? (
                    <RouterLink to={`/p/${projectId}/forms`} className="underline">
                      biểu mẫu {formTitle ?? `(${l.form_id})`}
                    </RouterLink>
                  ) : (
                    (l.target_url ?? '—')
                  )}
                </dd>
                <dt className="text-muted">Hạn dùng</dt>
                <dd>
                  {l.expires_at ? (
                    <>
                      {dateTime(l.expires_at)}
                      <span className="id-chip ml-1">sau đó redirect trả 410</span>
                    </>
                  ) : (
                    'không đặt hạn'
                  )}
                </dd>
                <dt className="text-muted">Tham số</dt>
                <dd className="text-muted">
                  utm_* gắn trên link rút gọn được chuyển tiếp sang đích; <code>cx</code> và{' '}
                  <code>src</code> là của riêng shortener, không chuyển tiếp.
                </dd>
              </dl>
              {mayDelete && <DeleteLink link={l} projectId={projectId} />}
            </Card>
          </div>

          {!mayReadStats ? (
            <Empty
              title="Không có quyền xem số liệu"
              hint="Đọc báo cáo lượt bấm cần quyền analytics.read. Các ô số để trống ở đây là do thiếu quyền, không phải vì link chưa ai bấm."
            />
          ) : (
            <>
              <div className="flex flex-wrap items-center gap-2">
                <span className="font-mono text-meta tracking-caps text-faint">KHOẢNG</span>
                {RANGES.map((r) => (
                  <button
                    key={r.days}
                    type="button"
                    aria-pressed={r.days === rangeDays}
                    className={`btn ${r.days === rangeDays ? 'bg-ink text-white' : ''}`}
                    onClick={() => setRangeDays(r.days)}
                  >
                    {r.label}
                  </button>
                ))}
                <span className="id-chip">gom theo {bucketLabel(range.bucket)}</span>
              </div>

              {stats.isPending && <Loading label="Đang tải số liệu…" />}
              {stats.isError && <ErrorBanner error={stats.error} retry={() => void stats.refetch()} />}
              {s && <StatsBody stats={s} />}
            </>
          )}
        </div>
      )}
    </div>
  )
}

/* ------------------------------------------------------------ stats body */

function StatsBody({ stats: s }: { stats: LinkStats }) {
  const qrSlice = s.sources.find((x) => x.key === 'qr')
  const qrClicks = qrSlice?.clicks ?? 0

  return (
    <>
      {/* The two totals, side by side and labelled, because they are not the
          same measurement and the gap between them is not an error. */}
      <div className="grid gap-3 sm:grid-cols-2">
        <Card title="Lượt bấm — toàn khoảng đã chọn" aside="nguồn: rollup">
          <p className="text-[22px] font-semibold leading-none">{num(s.clicks)}</p>
          <p className="mt-1 text-meta text-muted">
            Từ {date(s.from)} đến {date(s.to)}. Rollup không bị hạn lưu sự kiện thô giới hạn, nên đây
            là con số đầy đủ nhất về lượt bấm.
          </p>
          <p className="id-chip mt-1">
            lần đầu {s.first_click ? dateTime(s.first_click) : '—'} · gần nhất{' '}
            {s.last_click ? dateTime(s.last_click) : '—'}
          </p>
        </Card>

        <Card title="Lượt bấm dùng để phân tích" aside="nguồn: sự kiện thô">
          <p className="text-[22px] font-semibold leading-none">{num(s.breakdown_clicks)}</p>
          <p className="mt-1 text-meta text-muted">
            Chỉ từ {date(s.breakdown_at)} trở đi. Mọi tỉ lệ trong các bảng bên dưới chia cho con số
            này, không chia cho tổng lượt bấm — hai con số phủ hai khoảng khác nhau.
          </p>
          {s.clicks !== s.breakdown_clicks && (
            <p className="id-chip mt-1">
              chênh {num(s.clicks - s.breakdown_clicks)} lượt = phần lịch sử mà sự kiện thô không còn
              giữ (hoặc độ trễ rollup, tối đa 6 phút)
            </p>
          )}
        </Card>
      </div>

      <p role="status" className="rounded border border-line bg-panel px-3 py-2 text-meta text-muted">
        {s.breakdown_note}
      </p>

      <Card title="Lượt bấm theo thời gian" aside={`${s.points.length} mốc`}>
        <ClickChart points={s.points} />
      </Card>

      <div className="grid gap-3 sm:grid-cols-3">
        <Card title="Quét QR" aside="trong hạn lưu">
          <p className="text-[22px] font-semibold leading-none">{num(qrClicks)}</p>
          <p className="mt-1 text-meta text-muted">
            {pct(qrClicks, s.breakdown_clicks)} lượt bấm đo được đến từ mã QR (server trả{' '}
            {Math.round(s.qr_share * 100)}%). Một lượt quét xảy ra ngay trước tấm poster in mã, nên
            nó nói điều mà một cú bấm vào link được chia sẻ không nói.
          </p>
        </Card>

        <Card title="Dải mạng /24" aside="KHÔNG phải số người">
          <p className="text-[22px] font-semibold leading-none">{num(s.networks)}</p>
          <p className="mt-1 text-meta text-muted">
            Số dải mạng /24 khác nhau đã bấm link. Hệ thống cố ý không nhận ra người quay lại: mỗi
            lần chuyển hướng sinh một <code>visit_id</code> mới, nên không có con số “khách truy
            cập” nào ở đây cả.
          </p>
        </Card>

        <Card title="Lượt bấm mỗi dải mạng" aside="độ tập trung">
          <p className="text-[22px] font-semibold leading-none">
            {s.networks ? s.clicks_per_network.toLocaleString('vi-VN') : '—'}
          </p>
          <p className="mt-1 text-meta text-muted">
            {s.networks
              ? '900 lượt từ 4 dải là script hoặc một văn phòng sau NAT; 900 lượt từ 300 dải mới là độ phủ.'
              : 'Chưa đo được dải mạng nào trong khoảng này — không có mẫu số để chia.'}
          </p>
        </Card>
      </div>

      <div className="grid gap-3 md:grid-cols-2">
        <SliceTable
          title="Nguồn"
          hint="qr = quét mã in ra; direct = bấm thẳng vào link."
          rows={s.sources}
          total={s.breakdown_clicks}
          labelKey={sourceLabel}
        />
        <SliceTable
          title="Referrer"
          hint="Chỉ lưu host của trang dẫn tới — đường dẫn có thể chứa dữ liệu cá nhân do bên khác đặt vào."
          rows={s.referrers}
          total={s.breakdown_clicks}
          labelKey={(k) => k || 'không có referrer'}
        />
        <SliceTable
          title="Trình duyệt"
          hint="Chỉ giữ họ trình duyệt. Lưu nguyên chuỗi user agent là một dấu vân tay của người truy cập, đổi lại không thêm được gì cho phân tích."
          rows={s.browsers}
          total={s.breakdown_clicks}
          labelKey={(k) => k || 'không rõ'}
        />
        <SliceTable
          title="utm_source"
          hint="Đọc từ tham số trên chính link rút gọn, đồng thời vẫn được chuyển tiếp sang đích."
          rows={s.utm_sources}
          total={s.breakdown_clicks}
          labelKey={campaignLabel}
        />
        <SliceTable
          title="utm_medium"
          rows={s.utm_mediums}
          total={s.breakdown_clicks}
          labelKey={campaignLabel}
        />
        <SliceTable
          title="utm_campaign"
          rows={s.utm_campaigns}
          total={s.breakdown_clicks}
          labelKey={campaignLabel}
        />
      </div>
    </>
  )
}

function sourceLabel(k: string): string {
  if (k === 'qr') return 'quét QR'
  if (k === 'direct' || k === '') return 'bấm thẳng'
  return k
}

function campaignLabel(k: string): string {
  // The server groups clicks with no campaign under one key rather than dropping
  // them, so the column still adds up to the total.
  if (!k || k === 'none' || k === 'no campaign') return 'không gắn campaign'
  return k
}

function bucketLabel(bucket: string): string {
  return bucket === 'hour' ? 'giờ' : bucket === 'week' ? 'tuần' : 'ngày'
}

/* ------------------------------------------------------------ components */

/** A bar per bucket. No chart library -- this is one series of non-negative
 *  integers, and adding a dependency to draw it would cost more than it saves. */
function ClickChart({ points }: { points: ClickPoint[] }) {
  const max = points.reduce((m, p) => Math.max(m, p.clicks), 0)
  const total = points.reduce((sum, p) => sum + p.clicks, 0)
  const first = points.at(0)
  const last = points.at(-1)

  if (points.length === 0 || max === 0) {
    return (
      <Empty
        title="Không có lượt bấm nào trong khoảng này"
        hint="Chuỗi thời gian dựng từ rollup, nên khoảng trống ở đây nghĩa là thật sự không có lượt bấm, không phải do hết hạn lưu."
      />
    )
  }

  return (
    <div>
      <div
        role="img"
        aria-label={`Lượt bấm theo thời gian: tổng ${total.toLocaleString('vi-VN')} lượt, cao nhất ${max.toLocaleString('vi-VN')} lượt trong một mốc.`}
        className="flex h-24 items-end gap-px"
      >
        {points.map((p) => (
          <div
            key={p.bucket}
            className="min-w-px flex-1 bg-ink/80 hover:bg-accent"
            style={{ height: `${Math.max((p.clicks / max) * 100, p.clicks > 0 ? 2 : 0)}%` }}
            title={`${dateTime(p.bucket)} · ${p.clicks.toLocaleString('vi-VN')} lượt`}
          />
        ))}
      </div>
      <div className="mt-1 flex justify-between font-mono text-meta text-faint">
        <span>{first ? date(first.bucket) : ''}</span>
        <span>cao nhất {num(max)} lượt/mốc</span>
        <span>{last ? date(last.bucket) : ''}</span>
      </div>
    </div>
  )
}

/** One breakdown table.
 *
 * The network column carries its unit in the header on every table rather than
 * once at the top of the page: this is the number most likely to be copied into
 * a slide, and "mạng /24" travelling with it is the only thing stopping it from
 * being read as a headcount. */
function SliceTable({
  title,
  hint,
  rows,
  total,
  labelKey,
}: {
  title: string
  hint?: string
  rows: Slice[]
  total: number
  labelKey: (key: string) => string
}) {
  const sorted = [...rows].sort((a, b) => b.clicks - a.clicks)

  return (
    <Card title={title} aside={`/ ${num(total)} lượt đo được`}>
      {hint && <p className="mb-2 text-meta text-muted">{hint}</p>}
      {sorted.length === 0 ? (
        <p className="text-body text-muted">
          Chưa có dữ liệu trong hạn lưu sự kiện thô cho chiều này.
        </p>
      ) : (
        <Table
          head={
            <>
              <Th>Giá trị</Th>
              <Th className="text-right">Lượt bấm</Th>
              <Th className="text-right">Tỉ lệ</Th>
              <Th className="text-right">Dải /24</Th>
            </>
          }
        >
          {sorted.map((r) => (
            <Tr key={r.key || '(none)'}>
              <Td className="text-meta">{labelKey(r.key)}</Td>
              <Td className="text-right font-mono">{num(r.clicks)}</Td>
              <Td className="text-right font-mono">{pct(r.clicks, total)}</Td>
              <Td className="text-right font-mono">{num(r.networks)}</Td>
            </Tr>
          ))}
        </Table>
      )}
    </Card>
  )
}

/** The QR, plus a PNG the size a printer wants.
 *
 * Downloaded through a blob so the file lands with the code in its name. The
 * image lives on the link's own host, which is usually a different origin from
 * the admin panel, so the fetch can be refused; when it is, the plain link
 * below still opens the image and the person can save it by hand. */
function QRPanel({ link }: { link: LinkRow }) {
  const [size, setSize] = useState(1024)
  const [failed, setFailed] = useState(false)

  const download = useMutation({
    mutationFn: async () => {
      const res = await fetch(`${link.qr_url}?size=${size}`, { credentials: 'omit' })
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      const blob = await res.blob()
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = `qr-${link.code}-${size}.png`
      a.click()
      URL.revokeObjectURL(url)
    },
    onError: () => setFailed(true),
  })

  return (
    <Card title="Mã QR" aside={`${size}px`}>
      <div className="flex flex-col items-center gap-2">
        <QRImage url={link.qr_url} code={link.code} size={160} />
        <p className="text-center text-meta text-muted">
          Mã trỏ về <span className="font-mono">{bareShortURL(link.short_url)}?src=qr</span> — nhờ dấu{' '}
          <code>src=qr</code> mà lượt quét tách được khỏi lượt bấm thường.
        </p>
        <div className="flex w-full items-center gap-2">
          <label htmlFor="qr-size" className="text-meta font-semibold">
            Kích thước
          </label>
          <select
            id="qr-size"
            className="input"
            value={size}
            onChange={(e) => setSize(Number(e.target.value))}
          >
            <option value={512}>512 px — web</option>
            <option value={1024}>1024 px — tờ rơi</option>
            <option value={2048}>2048 px — poster</option>
          </select>
        </div>
        <button type="button" className="btn w-full" disabled={download.isPending} onClick={() => download.mutate()}>
          {download.isPending ? 'Đang tải…' : 'Tải PNG'}
        </button>
        {failed && (
          <p role="alert" className="text-meta text-duesoon">
            Trình duyệt không cho tải trực tiếp từ {hostOfShortURL(link.qr_url) || 'tên miền của link'}.{' '}
            <a className="underline" href={`${link.qr_url}?size=${size}`} target="_blank" rel="noreferrer">
              Mở ảnh
            </a>{' '}
            rồi lưu lại bằng tay.
          </p>
        )}
        <p className="id-chip text-center">
          Chỉ có PNG. Tài liệu API có nhắc <code>format=svg</code> nhưng endpoint hiện luôn trả PNG,
          nên nút SVG sẽ tải về một tệp sai định dạng.
        </p>
      </div>
    </Card>
  )
}

/** Deleting a link is not deleting a row: the code exists on paper.
 *
 * The confirmation says what a visitor will get afterwards, because that is the
 * consequence that leaves the building. */
function DeleteLink({ link, projectId }: { link: LinkRow; projectId: string | undefined }) {
  const qc = useQueryClient()
  const navigate = useNavigate()
  const [arming, setArming] = useState(false)

  const del = useMutation({
    mutationFn: async () => api.del<void>(`/api/v1/links/${link.id}`),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['links', projectId] })
      void qc.invalidateQueries({ queryKey: ['domains'] })
      void navigate(`/p/${projectId}/links`)
    },
  })

  if (!arming) {
    return (
      <div className="mt-3 border-t border-dashed border-line pt-3">
        <button type="button" className="btn text-overdue" onClick={() => setArming(true)}>
          Xoá link
        </button>
      </div>
    )
  }

  return (
    <div className="mt-3 rounded border border-overdue/40 bg-overdue/5 p-3">
      <p className="text-body font-semibold text-overdue">Xoá /{link.code}?</p>
      <p className="mt-0.5 text-meta text-muted">
        Redirect sẽ trả <span className="font-mono">410 Gone</span> — người quét mã đã in ra sẽ biết
        link đã kết thúc chứ không thấy 404. Số liệu lịch sử vẫn giữ. Mã sẽ không được cấp lại cho
        link khác trên cùng tên miền.
      </p>
      {del.isError && (
        <p role="alert" className="mt-1 text-meta text-overdue">
          {del.error instanceof RequestFailed ? del.error.body.title : 'Không xoá được.'}
        </p>
      )}
      <div className="mt-2 flex gap-2">
        <button type="button" className="btn text-overdue" disabled={del.isPending} onClick={() => del.mutate()}>
          {del.isPending ? 'Đang xoá…' : 'Xoá hẳn'}
        </button>
        <button type="button" className="btn" onClick={() => setArming(false)}>
          Giữ lại
        </button>
      </div>
    </div>
  )
}
