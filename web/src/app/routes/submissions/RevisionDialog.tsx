/**
 * Lịch sử chỉnh sửa — what was changed on this record, by whom, and from what.
 *
 * Until this screen existed the trail was real but unreadable: the rows were
 * written correctly and could only be got at with SQL. A record that can only be
 * audited by someone with database access is not one the organisation can show a
 * regulator, and it is not one the operator who made the change can check.
 *
 * It shows changed fields, not two blobs of JSON. Asking a reader to compare two
 * objects gets them to confirm whatever they already believed; the question here
 * is "what did somebody change", and the server answers it rather than leaving
 * it as an exercise.
 */
import { useQuery } from '@tanstack/react-query'
import { api } from '../../lib/api'
import { Callout, Card, ErrorBanner, Loading, SensitiveTag, dateTime } from '../../components/ui'

interface FieldChange {
  field_id: string
  label: string
  before: string
  after: string
  masked: boolean
}

interface Revision {
  id: string
  changed_at: string
  actor_type: string
  actor_id: string
  actor_name?: string
  source: string
  changes: FieldChange[]
}

/** How the change was made. The wording matters more than it looks: these are
 *  different events with different accountability, and the schema keeps them
 *  apart precisely so a reader can too. */
const SOURCES: Record<string, { label: string; meaning: string }> = {
  dsr_self_service: {
    label: 'Chủ thể tự sửa',
    meaning: 'Chủ thể dữ liệu đăng nhập cổng cá nhân bằng liên kết gửi tới địa chỉ của họ và tự sửa.',
  },
  dsr_operator: {
    label: 'Nhân viên sửa thay',
    meaning:
      'Chủ thể yêu cầu và nhân viên thực hiện. Cách xác minh người yêu cầu được ghi trong yêu cầu chỉnh sửa tương ứng.',
  },
  admin_edit: {
    label: 'Quản trị sửa trực tiếp',
    meaning: 'Sửa không qua yêu cầu chỉnh sửa nào. Hiện không có đường nào trong sản phẩm ghi ra giá trị này.',
  },
}

export function RevisionDialog({
  submissionId,
  revealSensitive,
  onClose,
}: {
  submissionId: string
  /** Passed through so the masked values match what the grid behind is showing;
   *  the server decides, this only asks. */
  revealSensitive: boolean
  onClose: () => void
}) {
  const q = useQuery({
    queryKey: ['revisions', submissionId, revealSensitive],
    queryFn: () =>
      api.get<{ data: Revision[] }>(
        `/api/v1/submissions/${submissionId}/revisions` +
          (revealSensitive ? '?include_sensitive=true' : ''),
      ),
  })

  const revisions = q.data?.data ?? []

  return (
    <div
      className="fixed inset-0 z-50 flex items-start justify-center overflow-y-auto bg-ink/40 p-6"
      role="dialog"
      aria-modal="true"
      aria-labelledby="revisions-title"
      onKeyDown={(e) => {
        if (e.key === 'Escape') onClose()
      }}
    >
      <div className="w-full max-w-2xl">
        <Card
          title={<span id="revisions-title">Lịch sử chỉnh sửa</span>}
          aside={revisions.length ? `${revisions.length} lần · mới nhất trước` : undefined}
        >
          <div className="grid gap-3">
            {q.isPending && <Loading />}
            {q.error && <ErrorBanner error={q.error} retry={() => void q.refetch()} />}

            {q.data && revisions.length === 0 && (
              <Callout tone="neutral" title="Bản ghi này chưa từng được sửa">
                Câu trả lời vẫn đúng như lúc người điền gửi đi.
              </Callout>
            )}

            {revisions.map((r) => (
              <RevisionEntry key={r.id} rev={r} />
            ))}

            {revisions.length > 0 && (
              <p className="border-t border-line pt-2 text-meta text-muted">
                Giá trị cũ được giữ lại chứ không bị ghi đè — đó là điều cho phép chứng minh hồ sơ
                từng mang giá trị nào. Xoá dữ liệu theo yêu cầu chủ thể sẽ xoá cả các bản ghi này.
              </p>
            )}

            <div>
              <button type="button" className="btn-primary" onClick={onClose}>
                Đóng
              </button>
            </div>
          </div>
        </Card>
      </div>
    </div>
  )
}

function RevisionEntry({ rev }: { rev: Revision }) {
  const src = SOURCES[rev.source] ?? {
    label: rev.source,
    meaning: 'Nguồn thay đổi không nằm trong danh sách đã biết.',
  }
  // A subject is identified by their internal id, not by a name: the identifier
  // they gave is stored as a one-way hash, so there is nothing to look up.
  const who =
    rev.actor_type === 'subject'
      ? 'chính chủ thể dữ liệu'
      : (rev.actor_name ?? `người dùng ${rev.actor_id.slice(0, 8)}`)

  return (
    <div className="rounded border border-line bg-panel px-3 py-2">
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <span className="text-body font-semibold">{who}</span>
        <span className="font-mono text-meta text-faint">{dateTime(rev.changed_at)}</span>
      </div>
      <p className="text-meta text-muted" title={src.meaning}>
        {src.label}
      </p>

      <dl className="mt-2 grid gap-1.5">
        {rev.changes.map((c) => (
          <div key={c.field_id} className="grid gap-0.5">
            <dt className="text-meta uppercase tracking-label text-faint">
              {c.label}
              {c.masked && (
                <>
                  {' '}
                  <SensitiveTag>đang che</SensitiveTag>
                </>
              )}
            </dt>
            <dd className="flex flex-wrap items-center gap-2 text-body">
              {c.masked ? (
                <span className="text-meta text-muted">
                  Field nhạy cảm — cần quyền submission.read_sensitive để xem giá trị.
                </span>
              ) : (
                <>
                  {/* Struck through, not merely greyed: two similar strings side
                      by side are read as one value repeated. */}
                  <span className="text-faint line-through">{c.before || '(trống)'}</span>
                  <span className="text-faint">→</span>
                  <span>{c.after || '(trống)'}</span>
                </>
              )}
            </dd>
          </div>
        ))}
      </dl>
    </div>
  )
}
