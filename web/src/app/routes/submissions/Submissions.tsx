/**
 * 1j — the submission table.
 *
 * The screen people live in. Three things it must not get wrong:
 *
 *  1. One table over many schema versions, with the reasons a cell is empty kept
 *     apart (see columns.ts).
 *  2. Sensitive fields masked by default, unmasked only against a capability the
 *     API reports, and labelled either way.
 *  3. A record's legal state stated in words, because "restricted" and "erased"
 *     change what anyone is allowed to do with the row.
 *
 * Paging is keyset, never offset: the table runs to hundreds of thousands of
 * rows and an offset scan over that is both slow and unstable while new
 * submissions arrive at the top.
 */
import { useState } from 'react'
import { Link, useParams } from 'react-router'
import { useQuery } from '@tanstack/react-query'
import { api, type List } from '../../lib/api'
import { can, useMe } from '../../lib/session'
import {
  Card,
  Empty,
  ErrorBanner,
  Field,
  Loading,
  PageHeader,
  SensitiveTag,
  num,
} from '../../components/ui'
import { SubmissionGrid } from './SubmissionGrid'
import { ExportDialog } from './ExportDialog'
import {
  buildRegistry,
  clipToFrom,
  cursorForTo,
  registrySpan,
  type GridPage,
} from './columns'

const PAGE_SIZE = 50

interface FormRow {
  id: string
  public_id: string
  title: string
  status: string
  live_version: number | null
  submission_count: number
}

export function Submissions() {
  const { projectId, formId } = useParams()
  const me = useMe()

  const forms = useQuery({
    queryKey: ['forms', projectId],
    queryFn: async () =>
      (await api.get<List<FormRow>>(`/api/v1/forms?project_id=${projectId}`)).data,
    enabled: Boolean(projectId),
  })

  // The sidebar links here without naming a form, and the API has no
  // cross-form grid. Rather than a dead link, offer the choice.
  if (!formId) return <FormPicker projectId={projectId} forms={forms} />

  if (me.isPending) return <Loading />
  if (!can(me.data, 'submission.read')) {
    return (
      <div className="p-6">
        <PageHeader title="Dữ liệu gửi về" />
        <Empty
          title="Bạn không có quyền xem dữ liệu gửi về"
          hint={
            <>
              Cần capability <span className="font-mono">submission.read</span> trên dự án này. Người quản
              trị tổ chức cấp được ở màn Thành viên &amp; quyền.
            </>
          }
        />
      </div>
    )
  }

  return (
    <Grid
      formId={formId}
      projectId={projectId}
      form={forms.data?.find((f) => f.id === formId)}
      canReadSensitive={can(me.data, 'submission.read_sensitive')}
      canExport={can(me.data, 'submission.export')}
      orgRole={me.data?.org_role}
    />
  )
}

function Grid({
  formId,
  projectId,
  form,
  canReadSensitive,
  canExport,
  orgRole,
}: {
  formId: string
  projectId: string | undefined
  form: FormRow | undefined
  canReadSensitive: boolean
  canExport: boolean
  orgRole: string | undefined
}) {
  const [from, setFrom] = useState('')
  const [to, setTo] = useState('')
  /** Cursors of the pages walked into, so "Trang trước" does not re-scan. */
  const [trail, setTrail] = useState<string[]>([])
  const [hiddenOverride, setHiddenOverride] = useState<Record<string, boolean>>({})
  const [reveal, setReveal] = useState(false)
  const [revealPrompt, setRevealPrompt] = useState(false)
  const [exporting, setExporting] = useState(false)

  const cursor = trail[trail.length - 1] ?? cursorForTo(to)

  const params = new URLSearchParams({ limit: String(PAGE_SIZE) })
  if (cursor) params.set('cursor', cursor)
  if (reveal) params.set('include_sensitive', 'true')

  const page = useQuery({
    queryKey: ['submissions', formId, cursor, reveal],
    queryFn: async () =>
      api.get<GridPage>(`/api/v1/forms/${formId}/submissions?${params.toString()}`),
  })

  const columns = buildRegistry(page.data?.columns ?? [])
  const visible = columns.filter((c) => hiddenOverride[c.key] ?? !c.hiddenByDefault)
  const sensitive = columns.filter((c) => c.sensitive)

  // The lower bound of the range is applied here because the API has no `from`.
  // Sound only because rows arrive strictly newest-first — see clipToFrom.
  const clipped = clipToFrom(page.data?.rows ?? [], from)
  const hasNext = Boolean(page.data?.next_cursor) && !clipped.reachedStart

  const resetRange = (next: { from?: string; to?: string }) => {
    if (next.from !== undefined) setFrom(next.from)
    if (next.to !== undefined) setTo(next.to)
    setTrail([])
  }

  return (
    <div className="p-6">
      <PageHeader
        title={form ? `${form.title} · dữ liệu gửi về` : 'Dữ liệu gửi về'}
        meta={
          <>
            {form ? `${num(form.submission_count)} bản ghi đang hoạt động` : 'đang tải số bản ghi'}
            {columns.length > 0 && ` · cột hợp nhất từ ${registrySpan(columns)}`}
          </>
        }
        actions={
          <>
            <ColumnPicker
              columns={columns}
              hiddenOverride={hiddenOverride}
              onToggle={(key, show) => setHiddenOverride((h) => ({ ...h, [key]: show }))}
            />
            <button
              type="button"
              className="btn-primary"
              disabled={!canExport}
              title={
                canExport
                  ? undefined
                  : 'Cần capability submission.export. Xuất file là trích xuất hàng loạt dữ liệu cá nhân.'
              }
              onClick={() => setExporting(true)}
            >
              Xuất Excel
            </button>
          </>
        }
      />

      <Card className="mb-3">
        <div className="flex flex-wrap items-end gap-3">
          <Field label="Từ ngày">
            <input
              type="date"
              className="input"
              value={from}
              max={to || undefined}
              onChange={(e) => resetRange({ from: e.target.value })}
            />
          </Field>
          <Field label="Đến ngày">
            <input
              type="date"
              className="input"
              value={to}
              min={from || undefined}
              onChange={(e) => resetRange({ to: e.target.value })}
            />
          </Field>
          {(from || to) && (
            <button type="button" className="btn" onClick={() => resetRange({ from: '', to: '' })}>
              Bỏ lọc ngày
            </button>
          )}

          <div className="ml-auto flex items-center gap-2">
            {sensitive.length > 0 && <SensitiveTag>{sensitive.length} field nhạy cảm</SensitiveTag>}
            {sensitive.length > 0 && canReadSensitive && (
              <button
                type="button"
                className="btn"
                onClick={() => (reveal ? setReveal(false) : setRevealPrompt(true))}
              >
                {reveal ? 'Che lại' : 'Hiện field nhạy cảm'}
              </button>
            )}
            {sensitive.length > 0 && !canReadSensitive && (
              <span className="text-meta text-muted">
                che vĩnh viễn — thiếu <span className="font-mono">submission.read_sensitive</span>
              </span>
            )}
          </div>
        </div>

        {revealPrompt && (
          <RevealPrompt
            columns={sensitive.map((c) => c.label)}
            onCancel={() => setRevealPrompt(false)}
            onConfirm={() => {
              setReveal(true)
              setRevealPrompt(false)
              setTrail([])
            }}
          />
        )}
      </Card>

      {page.isPending && <Loading label="Đang tải bản ghi…" />}
      {page.isError && <ErrorBanner error={page.error} retry={() => void page.refetch()} />}

      {page.data && clipped.rows.length === 0 && (
        // Three different emptinesses, and saying the wrong one is a statement
        // about the data rather than about the view. A page past the end used to
        // announce "biểu mẫu chưa nhận bản ghi nào" directly under a header
        // reading "25 bản ghi đang hoạt động".
        <Empty
          title={
            trail.length > 0
              ? 'Hết bản ghi ở trang này'
              : from || to
                ? 'Không có bản ghi nào trong khoảng ngày này'
                : 'Biểu mẫu chưa nhận bản ghi nào'
          }
          hint={
            trail.length > 0 ? (
              <>
                Bạn đã đi quá trang cuối.{' '}
                <button
                  type="button"
                  className="underline"
                  onClick={() => setTrail((t) => t.slice(0, -1))}
                >
                  Quay lại trang trước
                </button>
                .
              </>
            ) : from || to ? (
              'Bộ lọc ngày đang thu hẹp kết quả. Nới khoảng ngày hoặc bỏ lọc để xem toàn bộ.'
            ) : (
              'Biểu mẫu đã publish nhưng chưa ai gửi, hoặc link phát hành chưa được chạy.'
            )
          }
        />
      )}

      {page.data && clipped.rows.length > 0 && (
        <>
          <SubmissionGrid
            columns={visible}
            rows={clipped.rows}
            revealSensitive={reveal}
            onRequestReveal={canReadSensitive ? () => setRevealPrompt(true) : undefined}
          />

          <nav
            aria-label="Phân trang"
            className="mt-2 flex items-center gap-3 rounded border border-line bg-panel px-3 py-2"
          >
            <span className="id-chip">
              cursor phân trang · {num(clipped.rows.length)} dòng trên trang này
              {form ? ` / ${num(form.submission_count)}` : ''}
            </span>
            <div className="ml-auto flex gap-2">
              <button
                type="button"
                className="btn"
                disabled={trail.length === 0}
                onClick={() => setTrail((t) => t.slice(0, -1))}
              >
                Trang trước
              </button>
              <button
                type="button"
                className="btn"
                disabled={!hasNext}
                onClick={() => {
                  const next = page.data?.next_cursor
                  if (next) setTrail((t) => [...t, next])
                }}
              >
                Trang sau
              </button>
            </div>
          </nav>
          {clipped.reachedStart && (
            <p className="mt-1 text-meta text-muted">Đã tới đầu khoảng ngày đang lọc.</p>
          )}
        </>
      )}

      {exporting && (
        <ExportDialog
          formId={formId}
          formTitle={form?.title ?? 'Biểu mẫu'}
          projectId={projectId}
          from={from}
          to={to}
          sensitiveColumns={sensitive}
          canReadSensitive={canReadSensitive}
          orgRole={orgRole}
          activeCount={form?.submission_count}
          onClose={() => setExporting(false)}
        />
      )}
    </div>
  )
}

/**
 * Unmasking is a decision, so it gets a moment.
 *
 * Not a nag: it names which columns open and states that the act is recorded.
 * Anyone holding the capability will still click through — the point is that
 * they know afterwards what they looked at.
 */
function RevealPrompt({
  columns,
  onCancel,
  onConfirm,
}: {
  columns: string[]
  onCancel: () => void
  onConfirm: () => void
}) {
  return (
    <div role="alert" className="mt-3 rounded border border-accent bg-accent/5 px-3 py-2">
      <p className="text-body font-semibold text-legal">Mở lớp che dữ liệu nhạy cảm</p>
      <p className="mt-1 text-meta">
        {/* The question, not its id. Somebody deciding whether to open a
            person's identity card should be reading the question they answered,
            not fld_01KZ… -- and this dialog is the last point before the value
            is on screen and the read is in the audit log. */}
        Sẽ hiện rõ <span className="font-semibold">{columns.join(', ')}</span> cho các bản ghi trên trang đang
        xem. Đây là một lần đọc dữ liệu nhạy cảm và được ghi vào nhật ký audit.
      </p>
      <div className="mt-2 flex gap-2">
        <button type="button" className="btn" onClick={onCancel}>
          Giữ che
        </button>
        <button type="button" className="btn-primary" onClick={onConfirm}>
          Hiện
        </button>
      </div>
    </div>
  )
}

function ColumnPicker({
  columns,
  hiddenOverride,
  onToggle,
}: {
  columns: ReturnType<typeof buildRegistry>
  hiddenOverride: Record<string, boolean>
  onToggle: (key: string, show: boolean) => void
}) {
  const shown = columns.filter((c) => hiddenOverride[c.key] ?? !c.hiddenByDefault).length

  return (
    <details className="relative">
      <summary className="btn cursor-pointer list-none">
        Cột ({shown}/{columns.length}) ▾
      </summary>
      <div className="absolute right-0 z-10 mt-1 max-h-80 w-80 overflow-y-auto rounded border border-line bg-surface p-2 shadow-lg">
        {columns.length === 0 && <p className="text-meta text-muted">Chưa có version nào được publish.</p>}
        {columns.map((c) => (
          <label key={c.key} className="flex items-start gap-2 rounded px-1 py-1 text-meta hover:bg-chrome">
            <input
              type="checkbox"
              className="mt-0.5"
              checked={hiddenOverride[c.key] ?? !c.hiddenByDefault}
              onChange={(e) => onToggle(c.key, e.target.checked)}
            />
            <span className="min-w-0">
              <span className="font-mono text-meta">{c.fieldId}</span>
              {c.typeVariant && <span className="font-mono text-accent">@{c.typeVariant}</span>}
              {c.sensitive && <span className="ml-1 text-legal" aria-label="nhạy cảm">◆</span>}
              <span className="block text-muted">
                {c.label} · {c.type} · hỏi ở {c.versions}
                {/* A removed field is history, not a mistake: the answers stay,
                    and somebody may still have to erase them on request. */}
                {c.retiredAfter && ` · gỡ từ v${c.retiredAfter + 1}`}
              </span>
            </span>
          </label>
        ))}
      </div>
    </details>
  )
}

/** The project-level entry point has no form to show yet. */
function FormPicker({
  projectId,
  forms,
}: {
  projectId: string | undefined
  forms: ReturnType<typeof useQuery<FormRow[]>>
}) {
  return (
    <div className="p-6">
      <PageHeader title="Dữ liệu gửi về" meta="chọn một biểu mẫu" />
      {forms.isPending && <Loading />}
      {forms.isError && <ErrorBanner error={forms.error} retry={() => void forms.refetch()} />}
      {forms.data?.length === 0 && (
        <Empty
          title="Dự án chưa có biểu mẫu nào"
          hint="Dữ liệu gửi về luôn thuộc về một biểu mẫu cụ thể, vì cột của lưới lấy từ schema các version của nó."
        />
      )}
      {forms.data && forms.data.length > 0 && (
        <ul className="flex flex-col gap-1">
          {forms.data.map((f) => (
            <li key={f.id}>
              <Link
                to={`/p/${projectId}/forms/${f.id}/submissions`}
                className="flex items-baseline gap-2 rounded border border-line bg-surface px-3 py-2 text-body hover:bg-chrome"
              >
                <span className="font-semibold">{f.title}</span>
                <span className="id-chip">{f.public_id}</span>
                <span className="ml-auto font-mono text-meta">{num(f.submission_count)} bản ghi</span>
              </Link>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
