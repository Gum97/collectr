/**
 * Confirmation for an Excel export.
 *
 * An export is not a download button. It is a bulk extract of personal data, it
 * is written to an immutable audit log, and the organisation's DPO can read that
 * log. The dialog says so before the request is made rather than after, because
 * the only moment the warning can change a decision is this one.
 */
import { useState } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { api } from '../../lib/api'
import { Card, ErrorBanner, SensitiveTag, num } from '../../components/ui'
import type { GridColumn } from './columns'

interface ExportRequested {
  export_id: string
  status: string
  include_sensitive: boolean
  /** True when sensitive columns were asked for and refused; the file is still
   *  produced, with those columns masked. */
  masked: boolean
}

interface ExportJob {
  export_id: string
  status: 'queued' | 'running' | 'ready' | 'failed' | 'expired'
  row_count?: number
  download_url?: string
  expires_at?: string
  error?: string
}

/** Fixed by the report writer, not chosen here. Listed so nobody has to open the
 *  file to find out what is in it. */
const SHEETS = [
  'Tổng quan — funnel, tỉ lệ hoàn thành, số bản ghi bị loại',
  'Dữ liệu — lưới bản ghi, cột hợp nhất mọi version',
  'Phân tích theo câu hỏi — mẫu số là số người thực sự thấy câu hỏi',
  'Rơi rớt theo trang',
  'Đồng ý — tỉ lệ đồng ý và rút theo từng mục đích',
  'Thông tin xuất — ai xuất, lúc nào, bộ lọc nào',
]

export function ExportDialog({
  formId,
  formTitle,
  projectId,
  from,
  to,
  sensitiveColumns,
  canReadSensitive,
  orgRole,
  activeCount,
  onClose,
}: {
  formId: string
  formTitle: string
  projectId?: string
  from: string
  to: string
  sensitiveColumns: GridColumn[]
  canReadSensitive: boolean
  orgRole?: string
  activeCount?: number
  onClose: () => void
}) {
  const [includeSensitive, setIncludeSensitive] = useState(false)

  const request = useMutation({
    mutationFn: async () =>
      api.post<ExportRequested>(`/api/v1/forms/${formId}/exports`, {
        from,
        to,
        include_sensitive: includeSensitive,
        project_id: projectId ?? '',
      }),
  })

  const jobId = request.data?.export_id
  const job = useQuery({
    queryKey: ['export', jobId],
    queryFn: async () => api.get<ExportJob>(`/api/v1/exports/${jobId}`),
    enabled: Boolean(jobId),
    // Polled rather than pushed: the file is produced by a worker and there is
    // no socket. Stops the moment the job settles so a forgotten tab does not
    // poll a finished job all afternoon.
    refetchInterval: (query) => {
      const s = query.state.data?.status
      return s === 'queued' || s === 'running' ? 2000 : false
    },
  })

  return (
    <div
      className="fixed inset-0 z-50 flex items-start justify-center overflow-y-auto bg-ink/40 p-6"
      role="dialog"
      aria-modal="true"
      aria-labelledby="export-title"
      onKeyDown={(e) => {
        if (e.key === 'Escape') onClose()
      }}
    >
      <div className="w-full max-w-lg">
        <Card
          title={<span id="export-title">Xuất Excel</span>}
          aside={`${formTitle} · bộ lọc đang áp dụng`}
        >
          {!jobId ? (
            <div className="flex flex-col gap-3">
              <Warning />

              <RangeSummary from={from} to={to} activeCount={activeCount} />

              <section>
                <h3 className="font-mono text-meta tracking-caps text-faint">SHEET SẼ CÓ TRONG FILE</h3>
                <ul className="mt-1 flex flex-col gap-0.5 text-meta">
                  {SHEETS.map((s) => (
                    <li key={s} className="flex gap-1.5">
                      <span aria-hidden className="text-ok">
                        ✓
                      </span>
                      <span>{s}</span>
                    </li>
                  ))}
                </ul>
                <p className="mt-1 text-meta text-muted">
                  Bố cục workbook cố định — chưa chọn được từng sheet.
                </p>
              </section>

              <Permissions
                orgRole={orgRole}
                canReadSensitive={canReadSensitive}
                sensitiveColumns={sensitiveColumns}
                includeSensitive={includeSensitive}
                onToggleSensitive={setIncludeSensitive}
              />

              {request.isError && <ErrorBanner error={request.error} />}

              <div className="flex justify-end gap-2 border-t border-line pt-3">
                <button type="button" className="btn" onClick={onClose}>
                  Hủy
                </button>
                <button
                  type="button"
                  className="btn-primary"
                  disabled={request.isPending}
                  onClick={() => request.mutate()}
                >
                  {request.isPending ? 'Đang gửi…' : 'Tạo file xuất'}
                </button>
              </div>
            </div>
          ) : (
            <Progress
              job={job.data}
              error={job.error}
              masked={request.data?.masked === true}
              onClose={onClose}
            />
          )}
        </Card>
      </div>
    </div>
  )
}

function Warning() {
  return (
    <div role="alert" className="rounded border border-accent bg-accent/5 px-3 py-2">
      <p className="text-body font-semibold text-accent">Đây là truy cập hàng loạt dữ liệu cá nhân</p>
      <p className="mt-1 text-meta leading-relaxed">
        Hành động này ghi vào nhật ký bất biến: ai xuất, bao nhiêu bản ghi, bộ lọc nào, lúc nào. DPO của
        tổ chức xem được. File có hạn tải và tự xoá sau 24 giờ.
      </p>
    </div>
  )
}

function RangeSummary({
  from,
  to,
  activeCount,
}: {
  from: string
  to: string
  activeCount?: number
}) {
  return (
    <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-meta">
      <dt className="font-mono text-meta tracking-caps text-faint">KHOẢNG NGÀY</dt>
      <dd>{from || to ? `${from || 'từ đầu'} → ${to || 'tới nay'}` : 'toàn bộ thời gian'}</dd>
      <dt className="font-mono text-meta tracking-caps text-faint">SỐ DÒNG</dt>
      <dd>
        {/* Not a promise. The exact figure comes back on the finished job; a
            number guessed here would be the one people quote in a report. */}
        {activeCount === undefined
          ? 'chưa biết trước — số dòng thật hiện khi file xong'
          : `tối đa ${num(activeCount)} bản ghi đang hoạt động, khoảng ngày sẽ thu hẹp thêm`}
      </dd>
      <dt className="font-mono text-meta tracking-caps text-faint">BỊ LOẠI</dt>
      <dd>
        Bản ghi <strong>hạn chế xử lý</strong> và <strong>đã xoá</strong> không có trong file, kể cả khi
        đang nhìn thấy chúng trên lưới.
      </dd>
    </dl>
  )
}

function Permissions({
  orgRole,
  canReadSensitive,
  sensitiveColumns,
  includeSensitive,
  onToggleSensitive,
}: {
  orgRole?: string
  canReadSensitive: boolean
  sensitiveColumns: GridColumn[]
  includeSensitive: boolean
  onToggleSensitive: (v: boolean) => void
}) {
  const names = sensitiveColumns.map((c) => c.fieldId).join(', ')

  return (
    <section className="rounded border border-dashed border-faint px-3 py-2 text-meta">
      <p className="font-semibold">
        Quyền của bạn: <span className="id-chip">{orgRole ?? 'không rõ'}</span>
      </p>

      {sensitiveColumns.length === 0 ? (
        <p className="mt-1 text-muted">Biểu mẫu này không có field nhạy cảm.</p>
      ) : canReadSensitive ? (
        // Offered, never default. Holding the capability is permission to decide,
        // not a reason to put sensitive data in every file.
        <label className="mt-1.5 flex items-start gap-2">
          <input
            type="checkbox"
            checked={includeSensitive}
            onChange={(e) => onToggleSensitive(e.target.checked)}
            className="mt-0.5"
          />
          <span>
            Kèm cột nhạy cảm ({names}) ở dạng rõ. <SensitiveTag>nhạy cảm</SensitiveTag> Không tick thì các
            cột này ra <span className="font-mono">••••</span>.
          </span>
        </label>
      ) : (
        <p className="mt-1">
          Cột <span className="font-mono text-accent">{names}</span> sẽ ra{' '}
          <span className="font-mono">••••</span> — bạn không có{' '}
          <span className="font-mono">submission.read_sensitive</span>.
        </p>
      )}
    </section>
  )
}

function Progress({
  job,
  error,
  masked,
  onClose,
}: {
  job: ExportJob | undefined
  error: unknown
  masked: boolean
  onClose: () => void
}) {
  if (error) return <ErrorBanner error={error} />

  const status = job?.status ?? 'queued'
  const label: Record<ExportJob['status'], string> = {
    queued: 'Đã xếp hàng đợi…',
    running: 'Đang sinh file…',
    ready: 'File đã sẵn sàng.',
    failed: 'Không sinh được file.',
    expired: 'File đã hết hạn.',
  }

  return (
    <div className="flex flex-col gap-3">
      <p role="status" className="text-body font-semibold">
        {label[status]}
      </p>

      {masked && (
        <p className="text-meta text-muted">
          Bạn đã chọn kèm cột nhạy cảm nhưng không có <span className="font-mono">submission.read_sensitive</span>
          . File vẫn được tạo, các cột đó bị che.
        </p>
      )}

      {(status === 'queued' || status === 'running') && (
        <p className="text-meta text-muted">
          Yêu cầu đã được ghi vào nhật ký audit. Đóng hộp thoại không huỷ job — job vẫn chạy tiếp.
        </p>
      )}

      {status === 'ready' && (
        <div className="flex flex-col gap-1">
          <p className="text-meta">
            {job?.row_count === undefined ? 'Số dòng: —' : `${num(job.row_count)} dòng.`} Link tải dùng
            một lần và hết hạn cùng file.
          </p>
          {job?.download_url && (
            <a href={job.download_url} className="btn-primary w-fit no-underline" download>
              Tải file
            </a>
          )}
        </div>
      )}

      {status === 'failed' && (
        <div role="alert" className="rounded border border-overdue/40 bg-overdue/5 px-3 py-2 text-meta text-overdue">
          <p className="font-semibold">{job?.error || 'Worker báo lỗi không rõ.'}</p>
          <p className="mt-0.5">Yêu cầu xuất lại; nếu lặp lại, gửi mã job cho người vận hành.</p>
          <p className="id-chip mt-0.5 text-overdue/80">job: {job?.export_id}</p>
        </div>
      )}

      {status === 'expired' && (
        <p className="text-meta text-muted">
          File chứa dữ liệu cá nhân nên chỉ sống 24 giờ. Hãy yêu cầu xuất lại.
        </p>
      )}

      <div className="flex justify-end border-t border-line pt-3">
        <button type="button" className="btn" onClick={onClose}>
          Đóng
        </button>
      </div>
    </div>
  )
}
