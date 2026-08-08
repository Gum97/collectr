/**
 * 1o -- Link & QR, the list.
 *
 * Two numbers on this screen come from two different places and the screen says
 * so rather than averaging over the difference: the click counts arrive from
 * `GET /api/v1/links/stats?project_id=`, which is a windowed leaderboard built
 * on the rollups, while the rows themselves come from `GET /api/v1/links`, which
 * knows nothing about traffic. A link with no leaderboard entry has not been
 * measured in the window -- that is not the same as zero clicks, and it is
 * printed as an em dash.
 */
import { useMemo, useState } from 'react'
import { Link as RouterLink, useParams } from 'react-router'
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
} from '../../components/ui'

/* ------------------------------------------------------------------ types */

/** One link, exactly as `linkResponse` in internal/modules/links/api. */
export interface LinkRow {
  id: string
  code: string
  /** Absolute, on the link's own host -- never the host of the admin panel. */
  short_url: string
  qr_url: string
  target_url?: string
  form_id?: string
  expires_at?: string
  status: 'active' | 'disabled' | 'deleted' | 'legal_hold' | string
  created_at: string
}

export interface LinkListPage {
  data: LinkRow[]
  next_cursor?: string
}

/** One row of `GET /api/v1/links/stats?project_id=` -- a windowed leaderboard,
 *  not a lifetime total. `submits` and `conversion_rate` are absent for links
 *  that point at an external URL, where the ratio has no denominator. */
export interface TopLinkRow {
  link_id: string
  clicks: number
  submits?: number
  conversion_rate?: number
  last_seen: string | null
}

export interface DomainRow {
  id: string
  host: string
  is_default: boolean
  link_count: number
  short_url_example: string
}

export interface FormOption {
  id: string
  public_id: string
  title: string
  status: string
}

type Tone = 'neutral' | 'accent' | 'overdue' | 'duesoon' | 'ok'

/* -------------------------------------------------------------- utilities */

/** How the redirect will answer this code today.
 *
 * Phrased with the HTTP status a visitor actually receives, because that is
 * what the person reporting "my QR code stopped working" will have seen, and
 * because 410 and 451 mean different things to whoever has to fix it. */
export function linkState(l: LinkRow, now = Date.now()): { label: string; tone: Tone; dim: boolean } {
  if (l.status === 'legal_hold') return { label: '451 gỡ theo DSR', tone: 'overdue', dim: true }
  if (l.status === 'deleted') return { label: '410 đã xoá', tone: 'neutral', dim: true }
  if (l.status === 'disabled') return { label: '410 đã tắt', tone: 'neutral', dim: true }
  if (l.expires_at && new Date(l.expires_at).getTime() <= now) {
    return { label: '410 hết hạn', tone: 'duesoon', dim: true }
  }
  return { label: 'Hoạt động', tone: 'ok', dim: false }
}

/** The short URL without its scheme: `links.acme.vn/tet2026`.
 *  The scheme is noise in a dense table and the same on every row. */
export function bareShortURL(shortURL: string): string {
  return shortURL.replace(/^https?:\/\//, '').replace('/r/', '/')
}

export function hostOfShortURL(shortURL: string): string {
  try {
    return new URL(shortURL).host
  } catch {
    return ''
  }
}

/** Where a link sends someone, in one line.
 *
 * A form is named, not shown as a uuid: whoever prints the QR code chose the
 * form by its title and will look for that title here. */
export function targetLabel(l: LinkRow, forms: Map<string, FormOption>): string {
  if (l.form_id) {
    const f = forms.get(l.form_id)
    return f ? `biểu mẫu “${f.title}”` : 'biểu mẫu (không đọc được tên)'
  }
  if (!l.target_url) return 'không có đích'
  return l.target_url.replace(/^https?:\/\//, '')
}

export function useCopy() {
  const [copied, setCopied] = useState<string | null>(null)
  return {
    copied,
    copy: (text: string) => {
      void navigator.clipboard?.writeText(text).then(
        () => {
          setCopied(text)
          window.setTimeout(() => setCopied((c) => (c === text ? null : c)), 2000)
        },
        () => setCopied(null),
      )
    },
  }
}

function isoDay(offsetDays: number): string {
  const d = new Date(Date.now() + offsetDays * 86_400_000)
  return d.toISOString().slice(0, 10)
}

/* ---------------------------------------------------------------- screen */

const WINDOW_DAYS = 30

export function Links() {
  const { projectId } = useParams()
  const me = useMe()
  const mayReadStats = can(me.data, 'analytics.read')
  const mayWrite = can(me.data, 'link.write')
  const [q, setQ] = useState('')
  const [cursors, setCursors] = useState<string[]>([])

  const links = useQuery({
    queryKey: ['links', projectId, cursors.length],
    queryFn: async () => {
      const last = cursors.at(-1)
      const qs = new URLSearchParams({ project_id: projectId!, limit: '50' })
      if (last) qs.set('cursor', last)
      return api.get<LinkListPage>(`/api/v1/links?${qs.toString()}`)
    },
    enabled: Boolean(projectId),
  })

  // The window is sent explicitly rather than left to the server default so the
  // column header can name it. A click count whose period is unstated is the
  // number people quote in meetings and then cannot reproduce.
  const from = isoDay(-WINDOW_DAYS)
  const to = isoDay(0)
  const stats = useQuery({
    queryKey: ['link-leaderboard', projectId, from, to],
    queryFn: async () =>
      (
        await api.get<{ data: TopLinkRow[] }>(
          `/api/v1/links/stats?project_id=${projectId}&from=${from}T00:00:00Z&to=${to}T23:59:59Z`,
        )
      ).data,
    enabled: Boolean(projectId) && mayReadStats,
  })

  const domains = useQuery({
    queryKey: ['domains'],
    queryFn: async () => (await api.get<List<DomainRow>>('/api/v1/domains')).data,
    staleTime: 60_000,
  })

  const forms = useQuery({
    queryKey: ['forms', projectId],
    queryFn: async () => (await api.get<List<FormOption>>(`/api/v1/forms?project_id=${projectId}`)).data,
    enabled: Boolean(projectId),
  })

  const formsByID = useMemo(
    () => new Map((forms.data ?? []).map((f) => [f.id, f])),
    [forms.data],
  )
  const clicksByLink = useMemo(
    () => new Map((stats.data ?? []).map((s) => [s.link_id, s])),
    [stats.data],
  )

  const rows = useMemo(() => {
    const all = links.data?.data ?? []
    const needle = q.trim().toLowerCase()
    if (!needle) return all
    // Filtered here, not on the server: `q` is documented on the endpoint but the
    // handler currently ignores it, and sending a parameter that silently does
    // nothing is worse than filtering the page in hand and saying so.
    return all.filter(
      (l) =>
        l.code.toLowerCase().includes(needle) ||
        (l.target_url ?? '').toLowerCase().includes(needle) ||
        targetLabel(l, formsByID).toLowerCase().includes(needle),
    )
  }, [links.data, q, formsByID])

  const defaultDomain = domains.data?.find((d) => d.is_default)
  const featured = rows.at(0)

  return (
    <div className="p-6">
      <PageHeader
        title="Link & QR"
        meta={
          <>
            {links.data ? `${num(links.data.data.length)} link trên trang này` : '…'}
            {defaultDomain ? ` · miền mặc định ${defaultDomain.host}` : ' · chưa có tên miền'}
            {domains.data && domains.data.length > 1 ? ` · ${domains.data.length} tên miền` : ''}
          </>
        }
        actions={
          <>
            <RouterLink to="domains" className="btn">
              Tên miền
            </RouterLink>
            <ExportButton projectId={projectId} enabled={mayReadStats} from={from} to={to} />
            {mayWrite && (
              <RouterLink to="new" className="btn-primary">
                + Link mới
              </RouterLink>
            )}
          </>
        }
      />

      {links.isPending && <Loading />}
      {links.isError && <ErrorBanner error={links.error} retry={() => void links.refetch()} />}

      {featured && (
        <FeaturedLink
          link={featured}
          target={targetLabel(featured, formsByID)}
          summary={clicksByLink.get(featured.id)}
          mayReadStats={mayReadStats}
        />
      )}

      {links.data && (
        <>
          <div className="mb-2 mt-4 flex items-center gap-2">
            <label htmlFor="link-q" className="sr-only">
              Lọc link
            </label>
            <input
              id="link-q"
              className="input max-w-xs"
              placeholder="Lọc theo mã hoặc đích…"
              value={q}
              onChange={(e) => setQ(e.target.value)}
            />
            <span className="id-chip">lọc trong {num(links.data.data.length)} link đã tải</span>
          </div>

          {rows.length === 0 ? (
            <Empty
              title={q ? 'Không có link nào khớp' : 'Dự án này chưa có link nào'}
              hint={
                q
                  ? 'Bộ lọc chỉ chạy trên các link đã tải về trang này. Bấm “Tải thêm” rồi lọc lại nếu link cũ hơn.'
                  : 'Link rút gọn được tạo ở đây rồi mới in ra QR. Mỗi link nằm trên một tên miền và giữ tên miền đó suốt đời.'
              }
            />
          ) : (
            <Table
              head={
                <>
                  <Th>Mã · URL rút gọn</Th>
                  <Th>Đích</Th>
                  <Th className="text-right">Bấm ({WINDOW_DAYS} ngày)</Th>
                  <Th className="text-right">Gửi form</Th>
                  <Th>Trạng thái</Th>
                  <Th>Tạo · hạn dùng</Th>
                </>
              }
            >
              {rows.map((l) => {
                const st = linkState(l)
                const s = clicksByLink.get(l.id)
                return (
                  <Tr key={l.id} className={st.dim ? 'text-muted' : ''}>
                    <Td>
                      <RouterLink to={l.id} className="font-mono text-meta font-semibold hover:text-accent">
                        /{l.code}
                      </RouterLink>
                      <div className="id-chip">{bareShortURL(l.short_url)}</div>
                    </Td>
                    <Td className="text-meta">{targetLabel(l, formsByID)}</Td>
                    <Td className="text-right font-mono">
                      {/* An em dash, never 0: a link missing from the leaderboard
                          was not measured in this window, and printing zero turns
                          "not counted" into "nobody clicked". */}
                      {mayReadStats ? (s ? num(s.clicks) : '—') : '—'}
                    </Td>
                    <Td className="text-right font-mono">
                      {l.form_id ? (mayReadStats && s?.submits !== undefined ? num(s.submits) : '—') : ''}
                    </Td>
                    <Td>
                      <StatusPill tone={st.tone}>{st.label}</StatusPill>
                    </Td>
                    <Td className="text-meta">
                      {date(l.created_at)}
                      <div className="id-chip">
                        {l.expires_at ? `hết hạn ${date(l.expires_at)}` : 'không đặt hạn'}
                      </div>
                    </Td>
                  </Tr>
                )
              })}
            </Table>
          )}

          <div className="mt-3 flex items-center gap-2">
            {links.data.next_cursor && (
              <button
                type="button"
                className="btn"
                onClick={() => setCursors((c) => [...c, links.data.next_cursor!])}
              >
                Tải thêm
              </button>
            )}
            {cursors.length > 0 && (
              <button type="button" className="btn" onClick={() => setCursors([])}>
                Về đầu danh sách
              </button>
            )}
          </div>

          <div className="mt-3 space-y-1">
            <p className="id-chip">
              Mã trùng được giữa hai tên miền khác nhau — uniqueness theo (tên miền, mã).
            </p>
            <p className="id-chip">
              Cột “bấm” và “gửi form” lấy từ rollup, tính trong {WINDOW_DAYS} ngày gần nhất
              ({date(from)} – {date(to)}). Số lượt quét QR tách riêng chỉ có ở trang chi tiết
              từng link và trong báo cáo Excel, vì nó dựng từ sự kiện thô.
            </p>
            {!mayReadStats && (
              <p className="id-chip text-duesoon">
                ⚠ Bạn không có quyền <span className="font-semibold">analytics.read</span> nên các cột
                số để trống — không phải bằng 0.
              </p>
            )}
          </div>
        </>
      )}
    </div>
  )
}

/* ------------------------------------------------------------- sub-parts */

/** The most recent link, drawn with its QR the size it is actually scanned at.
 *
 * Present because the common task on this screen is not reading the table: it is
 * grabbing the code that was just made and putting it on something. */
function FeaturedLink({
  link,
  target,
  summary,
  mayReadStats,
}: {
  link: LinkRow
  target: string
  summary: TopLinkRow | undefined
  mayReadStats: boolean
}) {
  const { copied, copy } = useCopy()
  const st = linkState(link)

  return (
    <Card className="mt-1">
      <div className="flex gap-4">
        <QRImage url={link.qr_url} code={link.code} size={104} />
        <div className="min-w-0 flex-1">
          <div className="flex items-baseline gap-2">
            <span className="text-lede font-semibold">{bareShortURL(link.short_url)}</span>
            <StatusPill tone={st.tone}>{st.label}</StatusPill>
          </div>
          <p className="id-chip mt-0.5">
            → {target}
            {link.expires_at ? ` · hết hạn ${date(link.expires_at)}` : ' · không đặt hạn'}
          </p>

          <div className="mt-2 flex gap-5">
            <Stat label={`BẤM (${WINDOW_DAYS} NGÀY)`} value={mayReadStats ? summary?.clicks : undefined} />
            {link.form_id && (
              <Stat label={`GỬI FORM (${WINDOW_DAYS} NGÀY)`} value={mayReadStats ? summary?.submits : undefined} />
            )}
            <div>
              <div className="font-mono text-meta tracking-caps text-faint">LẦN CUỐI</div>
              <div className="text-lede font-semibold">
                {mayReadStats && summary?.last_seen ? dateTime(summary.last_seen) : '—'}
              </div>
            </div>
          </div>

          <div className="mt-2 flex flex-wrap gap-2">
            <RouterLink to={link.id} className="btn">
              Chi tiết & QR
            </RouterLink>
            <button type="button" className="btn" onClick={() => copy(link.short_url)}>
              {copied === link.short_url ? 'Đã sao chép' : 'Sao chép link'}
            </button>
          </div>
        </div>
      </div>
    </Card>
  )
}

function Stat({ label, value }: { label: string; value: number | undefined }) {
  return (
    <div>
      <div className="font-mono text-meta tracking-caps text-faint">{label}</div>
      <div className="text-lede font-semibold">{num(value ?? null)}</div>
    </div>
  )
}

/** The QR as the server renders it.
 *
 * Loaded from the link's own host, which is usually not the host serving this
 * page. When that request fails the box says so instead of leaving a broken
 * image icon, because a blank square next to a "Tải PNG" button reads as "the
 * code is empty" rather than "the image did not load". */
export function QRImage({ url, code, size }: { url: string; code: string; size: number }) {
  const [failed, setFailed] = useState(false)

  if (failed) {
    return (
      <div
        className="flex shrink-0 items-center justify-center rounded border border-dashed border-line p-2 text-center text-meta text-muted"
        style={{ width: size, height: size }}
      >
        Không tải được ảnh QR từ {hostOfShortURL(url) || 'tên miền của link'}
      </div>
    )
  }
  return (
    <img
      src={`${url}?size=${size * 3}`}
      width={size}
      height={size}
      onError={() => setFailed(true)}
      alt={`Mã QR của link /${code}`}
      className="shrink-0 rounded border border-line bg-white"
    />
  )
}

/** Queues the project's link report and follows the job to a download.
 *
 * The workbook is the only place per-link scan counts and network counts appear
 * side by side, so this is not a decorative button. */
function ExportButton({
  projectId,
  enabled,
  from,
  to,
}: {
  projectId: string | undefined
  enabled: boolean
  from: string
  to: string
}) {
  const qc = useQueryClient()
  const [jobID, setJobID] = useState<string | null>(null)

  const start = useMutation({
    mutationFn: async () =>
      api.post<{ export_id: string; status: string }>(`/api/v1/projects/${projectId}/link-exports`, {
        from,
        to,
      }),
    onSuccess: (res) => {
      setJobID(res.export_id)
      void qc.invalidateQueries({ queryKey: ['export', res.export_id] })
    },
  })

  const job = useQuery({
    queryKey: ['export', jobID],
    queryFn: async () =>
      api.get<{ status: string; download_url?: string; error?: string; row_count?: number }>(
        `/api/v1/exports/${jobID}`,
      ),
    enabled: Boolean(jobID),
    refetchInterval: (q) => {
      const s = q.state.data?.status
      return s === 'ready' || s === 'failed' || s === 'expired' ? false : 1500
    },
  })

  if (!enabled) return null

  if (job.data?.status === 'ready' && job.data.download_url) {
    return (
      <a className="btn" href={job.data.download_url}>
        Tải báo cáo ({num(job.data.row_count ?? null)} link)
      </a>
    )
  }
  if (job.data?.status === 'failed') {
    return (
      <span role="alert" className="id-chip text-overdue">
        Xuất báo cáo thất bại: {job.data.error ?? 'không rõ nguyên nhân'}
      </span>
    )
  }

  return (
    <button type="button" className="btn" disabled={start.isPending || Boolean(jobID)} onClick={() => start.mutate()}>
      {jobID ? 'Đang tạo báo cáo…' : start.isError ? exportError(start.error) : 'Xuất báo cáo'}
    </button>
  )
}

function exportError(err: unknown): string {
  if (err instanceof RequestFailed && err.status === 403) return 'Không đủ quyền xuất'
  return 'Thử xuất lại'
}
