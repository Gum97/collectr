/**
 * Screen 1n -- conversion funnel and per-page drop-off for one form.
 *
 * Four steps: a scan or click on the short link, the form being viewed, the
 * first answer being typed, the submission. Each step answers a different
 * question and collapsing any two of them hides a different failure: a poster
 * nobody scanned, a landing page nobody read, a first page nobody would fill in,
 * a last page nobody could finish.
 */
import { useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link, useParams } from 'react-router'
import { api, RequestFailed, type List } from '../../lib/api'
import {
  Card,
  Empty,
  ErrorBanner,
  Loading,
  PageHeader,
  StatusPill,
  date,
  num,
  pct,
} from '../../components/ui'
import { StepBars, TrendChart, type Series, type Step } from './Bars'
import { PageDropOff, type DropOffRow } from './PageDropOff'

interface FunnelPoint {
  bucket: string
  clicks: number
  views: number
  starts: number
  submits: number
}

interface FunnelResponse {
  from: string
  to: string
  /** From analytics.funnel_rollups: covers the whole period asked for. */
  clicks: number
  views: number
  starts: number
  submits: number
  /** From analytics.events, and therefore only as far back as the raw-event
   *  retention. Present only if the endpoint reports it. Never a denominator for
   *  anything measured above -- see ClickSources. */
  breakdown_clicks?: number | null
  /** Earliest moment the raw-event half can speak for. */
  breakdown_at?: string | null
  points?: FunnelPoint[] | null
  pages?: DropOffRow[] | null
}

/** One row of GET /api/v1/forms. */
interface FormRow {
  id: string
  title: string
  status: string
  submissions: number
}

/** GET /api/v1/forms/{id} -- only the title is needed here, and the detail
 *  endpoint does not return the submission count the list one does. */
interface FormDetail {
  id: string
  title: string
}

const PERIODS = [
  { days: 7, label: '7 ngày' },
  { days: 30, label: '30 ngày' },
  { days: 90, label: '90 ngày' },
]

const GROUPINGS = [
  { value: 'hour', label: 'theo giờ' },
  { value: 'day', label: 'theo ngày' },
  { value: 'week', label: 'theo tuần' },
]

export function Funnel() {
  const { projectId, formId } = useParams()
  if (!formId) return <FormPicker projectId={projectId} />
  return <FormFunnel formId={formId} />
}

/** `/p/:id/analytics` with no form chosen. The funnel is per form -- there is no
 *  project-wide version of it, because two forms in one project have nothing in
 *  common to add up. */
function FormPicker({ projectId }: { projectId: string | undefined }) {
  const forms = useQuery({
    queryKey: ['forms', projectId],
    queryFn: async () =>
      (await api.get<List<FormRow>>(`/api/v1/forms?project_id=${projectId}`)).data,
    enabled: Boolean(projectId),
  })

  return (
    <div className="p-6">
      <PageHeader title="Phân tích" meta="chọn một biểu mẫu để xem phễu và rơi rớt theo trang" />
      {forms.isPending && <Loading />}
      {forms.isError && <ErrorBanner error={forms.error} retry={() => void forms.refetch()} />}
      {forms.data?.length === 0 && (
        <Empty
          title="Dự án này chưa có biểu mẫu nào"
          hint="Phễu đo đường đi từ lượt bấm link tới lượt gửi, nên nó chỉ tồn tại khi đã có ít nhất một biểu mẫu."
        />
      )}
      {forms.data && forms.data.length > 0 && (
        <ul className="flex flex-col gap-2">
          {forms.data.map((f) => (
            <li key={f.id}>
              <Link
                to={`/p/${projectId}/analytics/${f.id}`}
                className="flex items-baseline justify-between rounded border border-line bg-surface px-3 py-2 hover:bg-chrome"
              >
                <span className="text-body font-semibold">{f.title}</span>
                <span className="id-chip">{num(f.submissions)} lượt gửi</span>
              </Link>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

function FormFunnel({ formId }: { formId: string }) {
  const [days, setDays] = useState(30)
  const [groupBy, setGroupBy] = useState('day')

  // Pinned per selection rather than recomputed each render: a range built from
  // Date.now() on every pass changes the query key on every pass, and the screen
  // refetches forever.
  const range = useMemo(() => {
    const to = new Date()
    const from = new Date(to.getTime() - days * 86_400_000)
    return { from: from.toISOString(), to: to.toISOString() }
  }, [days])

  const form = useQuery({
    queryKey: ['form', formId],
    queryFn: () => api.get<FormDetail>(`/api/v1/forms/${formId}`),
  })

  const funnel = useQuery({
    queryKey: ['funnel', formId, range.from, range.to, groupBy],
    queryFn: () =>
      api.get<FunnelResponse>(
        `/api/v1/forms/${formId}/analytics/funnel?from=${encodeURIComponent(range.from)}&to=${encodeURIComponent(range.to)}&group_by=${groupBy}`,
      ),
  })

  const d = funnel.data

  const steps: Step[] = [
    { key: 'clicks', label: 'Quét / bấm', value: d?.clicks ?? 0, note: 'rollup' },
    { key: 'views', label: 'Xem biểu mẫu', value: d?.views ?? 0, note: 'rollup' },
    { key: 'starts', label: 'Bắt đầu điền', value: d?.starts ?? 0, note: 'rollup' },
    { key: 'submits', label: 'Gửi', value: d?.submits ?? 0, note: 'rollup' },
  ]

  const points = d?.points ?? []
  const series: Series[] = [
    {
      key: 'views',
      label: 'Xem',
      values: points.map((p) => p.views),
      className: 'stroke-faint',
      dash: '2 3',
    },
    {
      key: 'starts',
      label: 'Bắt đầu điền',
      values: points.map((p) => p.starts),
      className: 'stroke-muted',
      dash: '6 3',
    },
    { key: 'submits', label: 'Gửi', values: points.map((p) => p.submits), className: 'stroke-ink' },
  ]

  return (
    <div className="p-6">
      <PageHeader
        title={`Phân tích · ${form.data?.title ?? 'Biểu mẫu'}`}
        meta="chậm ≤ 60s · analytics best-effort"
        actions={
          <>
            <label htmlFor="funnel-period" className="sr-only">
              Khoảng thời gian
            </label>
            <select
              id="funnel-period"
              className="input w-auto py-1 text-body"
              value={days}
              onChange={(e) => setDays(Number(e.target.value))}
            >
              {PERIODS.map((p) => (
                <option key={p.days} value={p.days}>
                  {p.label}
                </option>
              ))}
            </select>
            <label htmlFor="funnel-group" className="sr-only">
              Gộp theo
            </label>
            <select
              id="funnel-group"
              className="input w-auto py-1 text-body"
              value={groupBy}
              onChange={(e) => setGroupBy(e.target.value)}
            >
              {GROUPINGS.map((g) => (
                <option key={g.value} value={g.value}>
                  {g.label}
                </option>
              ))}
            </select>
          </>
        }
      />

      {funnel.isPending && <Loading label="Đang đọc số liệu phễu…" />}

      {funnel.isError && (
        <div className="flex flex-col gap-2">
          <ErrorBanner error={funnel.error} retry={() => void funnel.refetch()} />
          {/* Rule 4: an error has to say what to do about it. This particular one
              is usually a deployment that has the analytics store but has not
              mounted its read endpoint. */}
          {!(funnel.error instanceof RequestFailed) && (
            <p className="text-meta text-muted">
              Nếu lỗi lặp lại: bản triển khai này có thể chưa bật{' '}
              <code className="font-mono">GET /api/v1/forms/{'{id}'}/analytics/funnel</code>. Số
              liệu vẫn đang được ghi, chỉ là chưa đọc ra được — báo cho người vận hành deployment.
            </p>
          )}
        </div>
      )}

      {d && (
        <div className="flex flex-col gap-4">
          <Card title="Phễu chuyển đổi" aside={`${date(d.from)} → ${date(d.to)}`}>
            <StepBars steps={steps} base={d.clicks} />
            <ClickSources
              clicks={d.clicks}
              breakdownClicks={d.breakdown_clicks}
              breakdownAt={d.breakdown_at}
            />
            {d.clicks === 0 && d.views > 0 && (
              <p className="mt-2 text-meta text-muted">
                Không có lượt bấm nào trong kỳ, nên cột tỉ lệ hiện <span className="font-mono">—</span>{' '}
                thay vì 0%: biểu mẫu này đang được mở thẳng chứ không qua link rút gọn, và lấy 0 làm
                mẫu số sẽ biến mọi bước sau thành “0% chuyển đổi”.
              </p>
            )}
          </Card>

          <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
            <Rate
              label="Tỉ lệ hoàn thành"
              value={pct(d.submits, d.views)}
              detail={`${num(d.submits)} lượt gửi trên ${num(d.views)} lượt xem`}
              hint="Chỉ số sức khoẻ chính của biểu mẫu."
            />
            <Rate
              label="Tỉ lệ bỏ giữa chừng"
              value={pct(Math.max(0, d.starts - d.submits), d.starts)}
              detail={`${num(Math.max(0, d.starts - d.submits))} lượt bỏ dở trên ${num(d.starts)} lượt bắt đầu`}
              hint="Tách riêng khỏi tỉ lệ hoàn thành: “không thèm bắt đầu” và “bắt đầu rồi bỏ” là hai vấn đề khác nhau."
            />
          </div>

          <Card title="Theo thời gian" aside={GROUPINGS.find((g) => g.value === groupBy)?.label}>
            <TrendChart
              buckets={points.map((p) => date(p.bucket))}
              series={series}
              label={`Lượt xem, lượt bắt đầu điền và lượt gửi theo ${
                GROUPINGS.find((g) => g.value === groupBy)?.label ?? 'thời gian'
              }`}
            />
          </Card>

          <PageDropOff rows={d.pages} coverageFrom={d.breakdown_at} />

          <Card title="Đồng ý theo mục đích">
            <Empty
              title="Chưa có nguồn số liệu cho phần này"
              hint="Tỉ lệ đồng ý từng mục đích và số lượt rút sau đó hiện chỉ có trong workbook báo cáo (doc 09 §9.3, sheet Đồng ý); chưa có endpoint nào trả chúng ra cho màn hình này."
            />
          </Card>
        </div>
      )}
    </div>
  )
}

/**
 * The two click totals, when the endpoint reports both.
 *
 * They disagree by exactly the history the raw events no longer hold, and the
 * disagreement is the point: shown as two lines with two coverage periods, never
 * as one number, and never divided by each other.
 */
function ClickSources({
  clicks,
  breakdownClicks,
  breakdownAt,
}: {
  clicks: number
  breakdownClicks?: number | null
  breakdownAt?: string | null
}) {
  if (breakdownClicks === null || breakdownClicks === undefined) {
    return (
      <p className="mt-3 border-t border-dashed border-line pt-2 text-meta text-muted">
        Bốn bước trên đều đọc từ bảng rollup và phủ trọn kỳ đang chọn.
      </p>
    )
  }

  return (
    <div className="mt-3 border-t border-dashed border-line pt-2">
      <p className="text-meta font-semibold">Lượt bấm — hai con số, hai phạm vi:</p>
      <dl className="mt-1 grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-meta">
        <dt className="font-mono">{num(clicks)}</dt>
        <dd className="text-muted">bảng rollup · phủ trọn kỳ đang chọn</dd>
        <dt className="font-mono">{num(breakdownClicks)}</dt>
        <dd className="text-muted">
          sự kiện thô · chỉ từ {breakdownAt ? date(breakdownAt) : 'đầu hạn lưu sự kiện thô'} trở đi
        </dd>
      </dl>
      <p className="mt-1 text-meta text-muted">
        Hai con số này không được chia cho nhau: chúng đếm cùng một loại sự kiện nhưng phủ hai
        khoảng thời gian khác nhau. Chênh lệch tới 6 phút giữa hai nguồn là bình thường.
      </p>
    </div>
  )
}

function Rate({
  label,
  value,
  detail,
  hint,
}: {
  label: string
  value: string
  detail: string
  hint: string
}) {
  return (
    <section className="rounded border border-dashed border-line bg-surface p-3">
      <h3 className="font-mono text-meta tracking-caps text-faint">{label.toUpperCase()}</h3>
      <div className="flex items-baseline gap-2">
        <span className="text-[20px] font-semibold">{value}</span>
        {value === '—' && <StatusPill>chưa có mẫu số</StatusPill>}
      </div>
      {/* The denominator is printed next to every rate. A rate whose denominator
          is invisible is the one that gets quoted in a meeting a month later. */}
      <p className="id-chip">{detail}</p>
      <p className="mt-1 text-meta text-muted">{hint}</p>
    </section>
  )
}
