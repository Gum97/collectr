/**
 * Sửa dữ liệu theo yêu cầu của chủ thể — the screen for "khách gọi lên bảo em
 * nhập nhầm số".
 *
 * This is not an edit screen and is deliberately shaped so it cannot be mistaken
 * for one. An operator cannot open a record and change it: every write through
 * here raises a rectification request, names how the caller was verified, and
 * commits both together. The request is the lawful basis and the paper trail at
 * once, and the subject is told afterwards at the address on file.
 *
 * Only edited fields are sent. The server merges them over the stored answers,
 * which matters more than it looks: this screen never has the whole record --
 * sensitive answers are masked and some columns may be hidden from this reader
 * -- so sending everything it can see would write mask strings into the record
 * and blank whatever it could not see. Nothing would error.
 */
import { useState } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { api, RequestFailed } from '../../lib/api'
import { Callout, Card, ErrorBanner, Field, SensitiveTag } from '../../components/ui'
import { cellView, type ApiRow, type GridColumn } from './columns'

interface VerificationMethod {
  code: string
  label: string
}

interface RectifyResult {
  request_id: string
  /** Addresses the notice actually reached. Empty is a normal outcome — a form
   *  identified by phone has no channel here — and is shown, not hidden. */
  notified: string[] | null
  message: string
}

export function RectifyDialog({
  row,
  columns,
  revealSensitive,
  onClose,
  onDone,
}: {
  row: ApiRow
  columns: GridColumn[]
  revealSensitive: boolean
  onClose: () => void
  onDone: () => void
}) {
  const [edits, setEdits] = useState<Record<string, string>>({})
  const [verification, setVerification] = useState('')
  const [note, setNote] = useState('')
  const [result, setResult] = useState<RectifyResult | null>(null)

  const methods = useQuery({
    queryKey: ['verification-methods'],
    queryFn: () =>
      api.get<{ data: VerificationMethod[] }>('/api/v1/dsr/verification-methods'),
  })

  const submit = useMutation({
    mutationFn: () =>
      api.post<RectifyResult>(`/api/v1/dsr/submissions/${row.subject_id}/rectify`, {
        submission_id: row.id,
        answers: edits,
        verification_method: verification,
        note: note.trim(),
      }),
    onSuccess: (r) => {
      setResult(r)
      onDone()
    },
  })

  // Sensitive columns are listed but not editable. They are sealed under the
  // subject's own key and this path cannot re-seal them -- writing one into the
  // plaintext column would leave a readable copy beside the ciphertext, and
  // erasure would go on reporting success while the value survived it in clear.
  //
  // Attachments are excluded for a different reason and the server enforces it
  // too: the stored answer is a reference, not a value, and there is no upload
  // here. A text box over it would replace the reference with whatever was
  // typed, orphaning the file without any error anywhere.
  const editable = columns.filter((c) => !c.sensitive && c.type !== 'file')
  const sealed = columns.filter((c) => c.sensitive)
  const attachments = columns.filter((c) => !c.sensitive && c.type === 'file')
  const changed = Object.keys(edits)
  const fieldErrors = submit.error instanceof RequestFailed ? (submit.error.fields ?? {}) : {}

  return (
    <div
      className="fixed inset-0 z-50 flex items-start justify-center overflow-y-auto bg-ink/40 p-6"
      role="dialog"
      aria-modal="true"
      aria-labelledby="rectify-title"
      onKeyDown={(e) => {
        if (e.key === 'Escape') onClose()
      }}
    >
      <div className="w-full max-w-2xl">
        <Card
          title={<span id="rectify-title">Sửa theo yêu cầu chủ thể</span>}
          aside="ghi thành một yêu cầu chỉnh sửa"
        >
          {result ? (
            <Done result={result} onClose={onClose} />
          ) : (
            <div className="grid gap-4">
              <Callout tone="neutral" title="Đây không phải màn sửa dữ liệu tự do">
                Mỗi lần lưu ở đây tạo một <span className="font-semibold">yêu cầu chỉnh sửa</span>{' '}
                đứng tên chủ thể dữ liệu, ghi lại bạn là người sửa và bạn đã xác minh người gọi bằng
                cách nào. Giá trị cũ được giữ trong lịch sử sửa đổi, và chủ thể được gửi thông báo.
              </Callout>

              <div className="grid gap-3">
                {editable.map((c) => {
                  const view = cellView(c, row, { revealSensitive })
                  const current = typeof row.cells[c.fieldId]?.value === 'string'
                    ? String(row.cells[c.fieldId]?.value)
                    : ''
                  const touched = c.fieldId in edits
                  return (
                    <Field
                      key={c.key}
                      label={c.label}
                      error={fieldErrors[c.fieldId]}
                      hint={
                        touched
                          ? `Giá trị cũ: ${view.text}`
                          : 'Để nguyên nếu không sửa — ô không đụng tới sẽ giữ y như cũ.'
                      }
                    >
                      <input
                        className={`input ${touched ? 'border-accent' : ''}`}
                        defaultValue={current}
                        onChange={(e) => {
                          const next = { ...edits }
                          // Typing back to the original value takes the field out
                          // of the request entirely: a revision row that lists a
                          // field nobody changed makes the trail harder to read.
                          if (e.target.value === current) delete next[c.fieldId]
                          else next[c.fieldId] = e.target.value
                          setEdits(next)
                        }}
                      />
                    </Field>
                  )
                })}
              </div>

              {sealed.length > 0 && (
                <p className="flex flex-wrap items-center gap-1 rounded border border-dashed border-line bg-panel px-3 py-2 text-meta text-muted">
                  <SensitiveTag>không sửa được ở đây</SensitiveTag>
                  <span>
                    {sealed.map((c) => c.label).join(', ')} — được niêm phong bằng khoá của chính chủ
                    thể. Chủ thể tự sửa qua cổng dữ liệu cá nhân; đường này không niêm phong lại
                    được.
                  </span>
                </p>
              )}

              {attachments.length > 0 && (
                <p className="rounded border border-dashed border-line bg-panel px-3 py-2 text-meta text-muted">
                  <span className="font-semibold text-ink">
                    {attachments.map((c) => c.label).join(', ')}
                  </span>{' '}
                  — tệp đính kèm, không sửa được bằng cách gõ. Muốn thay tài liệu thì chủ thể nộp
                  lại bản mới; sửa ở đây chỉ đổi được các câu trả lời dạng chữ.
                </p>
              )}

              <Field
                label="Đã xác minh người yêu cầu bằng cách nào"
                error={fieldErrors['verification_method']}
                hint="Bắt buộc. Đây là câu trả lời cho “dựa vào đâu bạn tin đây đúng là họ” khi bản ghi này được rà soát về sau."
              >
                <select
                  className="input"
                  value={verification}
                  onChange={(e) => setVerification(e.target.value)}
                >
                  <option value="">— chọn cách xác minh —</option>
                  {(methods.data?.data ?? []).map((m) => (
                    <option key={m.code} value={m.code}>
                      {m.label}
                    </option>
                  ))}
                </select>
              </Field>

              <Field label="Ghi chú" hint="Tuỳ chọn. Ví dụ: số ticket, tên người gọi.">
                <input className="input" value={note} onChange={(e) => setNote(e.target.value)} />
              </Field>

              {submit.error && !Object.keys(fieldErrors).length && (
                <ErrorBanner error={submit.error} />
              )}

              <div className="flex items-center gap-3">
                <button
                  type="button"
                  className="btn-primary"
                  disabled={submit.isPending || changed.length === 0 || verification === ''}
                  onClick={() => submit.mutate()}
                >
                  {submit.isPending ? 'Đang lưu…' : `Sửa ${changed.length} ô và ghi nhận yêu cầu`}
                </button>
                <button type="button" className="btn-ghost" onClick={onClose}>
                  Huỷ
                </button>
                {changed.length === 0 && (
                  <span className="text-meta text-muted">Chưa sửa ô nào.</span>
                )}
              </div>
            </div>
          )}
        </Card>
      </div>
    </div>
  )
}

/**
 * What happened, including the part that did not.
 *
 * Whether the subject was actually reached is reported rather than assumed. An
 * operator who believes a notice went out, when none did, will not make the
 * phone call that covers it.
 */
function Done({ result, onClose }: { result: RectifyResult; onClose: () => void }) {
  const notified = result.notified ?? []
  return (
    <div className="grid gap-3">
      <Callout tone="ok" title="Đã sửa và ghi vào nhật ký">
        {result.message}
      </Callout>
      <dl className="grid gap-1 text-meta text-muted">
        <div className="flex gap-2">
          <dt className="w-32 shrink-0">Mã yêu cầu</dt>
          <dd className="font-mono text-ink">{result.request_id}</dd>
        </div>
        <div className="flex gap-2">
          <dt className="w-32 shrink-0">Đã báo chủ thể</dt>
          <dd className={notified.length ? 'text-ink' : 'text-overdue'}>
            {notified.length ? notified.join(', ') : 'chưa gửi được — hãy báo trực tiếp'}
          </dd>
        </div>
      </dl>
      <div>
        <button type="button" className="btn-primary" onClick={onClose}>
          Xong
        </button>
      </div>
    </div>
  )
}
