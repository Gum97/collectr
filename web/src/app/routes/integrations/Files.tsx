/**
 * Screen 2e -- attachment fields: what a question accepts, and what it received.
 *
 * The framing that matters here is in the second half of the screen: an uploaded
 * file is personal data in exactly the way an answer is. A scan of a national ID
 * card is not "an attachment", it is sensitive personal data that happens to be
 * stored as bytes, and it is erased when its subject asks and expires with the
 * submission's retention. The screen says so rather than assuming the reader
 * already made that connection.
 */
import { useState } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { Link, useParams } from 'react-router'
import { api, RequestFailed } from '../../lib/api'
import { useMe } from '../../lib/session'
import {
  Card,
  Empty,
  ErrorBanner,
  Field,
  Loading,
  PageHeader,
  SensitiveTag,
  StatusPill,
  Table,
  Td,
  Th,
  Tr,
  dateTime,
} from '../../components/ui'

interface FormMeta {
  id: string
  project_id: string
  public_id: string
  title: string
  status: string
  retention_days: number | null
  retention_action: string
  /** The working copy. Read from the admin endpoint on purpose: the public one
   *  records a form_view, so reading a schema from it would add a phantom
   *  visitor to the funnel every time this tab is opened. */
  draft_schema: Schema | null
  live_version_no: number
  live_schema: Schema | null
  has_draft: boolean
}

/**
 * The field shape as the Go schema serialises it.
 *
 * Declared locally rather than imported from shared/engine.ts: that module
 * describes the upload limit as `max_bytes`, while the server's schema tag is
 * `max_mb`. Writing the wrong key here would save a draft whose file limit
 * silently reverts to "no question-level limit".
 */
interface SchemaField {
  type: string
  label: string
  required?: boolean
  sensitive?: boolean
  pii?: string
  accept?: string[]
  max_mb?: number
}

interface Schema {
  fields: Record<string, SchemaField>
  [key: string]: unknown
}

interface ValidationResult {
  ok: boolean
  issues?: { code: string; severity: string; target?: string; message: string }[]
}

/** Every type the server's magic-byte detector recognises. Nothing outside this
 *  list can be accepted, whatever is typed into a form builder. */
const DETECTABLE = [
  { type: 'application/pdf', label: 'PDF' },
  { type: 'image/jpeg', label: 'JPEG' },
  { type: 'image/png', label: 'PNG' },
  { type: 'image/webp', label: 'WebP' },
  { type: 'image/heic', label: 'HEIC (ảnh iPhone)' },
]

/** The system ceiling, from files/domain.MaxUploadBytes. */
const SYSTEM_MAX_MB = 25
const SIZE_CHOICES = [1, 2, 5, 10, 25]

export function Files() {
  const { projectId, formId } = useParams()
  const me = useMe()
  const canWrite = Boolean(me.data?.capabilities.includes('form.write'))

  const form = useQuery({
    queryKey: ['form', formId],
    queryFn: async () => api.get<FormMeta>(`/api/v1/forms/${formId}`),
    enabled: Boolean(formId),
  })

  // Draft first, live as the fallback.
  //
  // A form published and never edited since has no draft at all, and reading
  // fields off an absent schema threw before the first paint -- a blank page
  // with nothing on it to explain itself. The two are kept distinct rather than
  // merged: editing writes to the draft, and which one is on screen decides
  // whether "save" creates a draft or overwrites one.
  const editing = Boolean(form.data?.has_draft)
  const schema = (editing ? form.data?.draft_schema : form.data?.live_schema) ?? undefined
  // fields is null on an empty schema, which Object.entries refuses.
  const fileFields = Object.entries(schema?.fields ?? {}).filter(([, f]) => f.type === 'file')

  return (
    <div className="p-6">
      <PageHeader
        title="Trường tệp đính kèm"
        meta={
          <>
            {form.data ? `${form.data.title} · ` : ''}
            POST /api/pub/forms/{'{public_id}'}/uploads
            {form.data ? ` · bản nháp, live v${form.data.live_version_no || '—'}` : ''}
          </>
        }
        actions={
          <Link to={`/p/${projectId}/forms`} className="btn">
            ← Biểu mẫu
          </Link>
        }
      />

      {form.isPending && <Loading />}
      {form.isError && <ErrorBanner error={form.error} retry={() => form.refetch()} />}
      {form.isError && (
        <ErrorBanner error={form.error} retry={() => form.refetch()} />
      )}

      {schema && fileFields.length === 0 && (
        <Empty
          title="Biểu mẫu này không có câu hỏi kiểu tệp"
          hint="Bản đang chạy không hỏi tệp nào, nên không có cấu hình để đặt và không thể có tệp nào được nhận. Thêm một câu hỏi kiểu file ở trình dựng biểu mẫu trước."
        />
      )}

      {schema && fileFields.length > 0 && (
        <div className="grid gap-4">
          {fileFields.map(([id, field]) => (
            <FieldConfig
              key={id}
              formId={formId ?? ''}
              fieldId={id}
              field={field}
              schema={schema}
              canWrite={canWrite}
            />
          ))}

          <ReceivedFiles formId={formId ?? ''} />

          <AttachmentsArePersonalData form={form.data} />
        </div>
      )}
    </div>
  )
}

function FieldConfig({
  formId,
  fieldId,
  field,
  schema,
  canWrite,
}: {
  formId: string
  fieldId: string
  field: SchemaField
  schema: Schema
  canWrite: boolean
}) {
  const [accept, setAccept] = useState<string[]>(field.accept ?? [])
  const [maxMB, setMaxMB] = useState<number>(field.max_mb ?? 0)
  const [required, setRequired] = useState<boolean>(field.required ?? false)

  const save = useMutation({
    mutationFn: (): Promise<ValidationResult> => {
      const next: SchemaField = { ...field, required, accept, max_mb: maxMB }
      if (accept.length === 0) delete next.accept
      if (!maxMB) delete next.max_mb
      if (!required) delete next.required
      return api.put<ValidationResult>(`/api/v1/forms/${formId}/draft`, {
        ...schema,
        fields: { ...(schema?.fields ?? {}), [fieldId]: next },
      })
    },
  })

  const fieldErrors = save.error instanceof RequestFailed ? save.error.fields : {}
  const dirty =
    required !== (field.required ?? false) ||
    maxMB !== (field.max_mb ?? 0) ||
    accept.join(',') !== (field.accept ?? []).join(',')

  return (
    <Card
      title={
        <span className="flex items-center gap-2">
          Câu hỏi: {field.label}
          {field.sensitive && <SensitiveTag />}
        </span>
      }
      aside={
        <>
          {fieldId} · kiểu file
          {field.pii ? ` · pii: ${field.pii}` : ''}
        </>
      }
    >
      <div className="grid gap-3 md:grid-cols-[minmax(0,1fr)_180px]">
        <fieldset className="min-w-0">
          <legend className="text-meta font-semibold">Loại tệp chấp nhận</legend>
          <div className="mt-1.5 flex flex-wrap gap-1.5">
            {DETECTABLE.map((t) => {
              const on = accept.includes(t.type)
              return (
                <button
                  key={t.type}
                  type="button"
                  disabled={!canWrite}
                  aria-pressed={on}
                  onClick={() =>
                    setAccept((prev) =>
                      on ? prev.filter((a) => a !== t.type) : [...prev, t.type],
                    )
                  }
                  className={`rounded border px-2 py-1 font-mono text-meta disabled:cursor-not-allowed disabled:opacity-50 ${
                    on ? 'border-line bg-ink text-white' : 'border-faint text-muted hover:bg-chrome'
                  }`}
                >
                  {t.type} {on ? '✕' : '+'}
                </button>
              )
            })}
          </div>
          <p className="mt-1.5 text-meta text-muted">
            Kiểu tệp được xác định bằng nội dung thật của tệp (magic bytes), không tin phần mở
            rộng hay <span className="font-mono">Content-Type</span> trình duyệt gửi lên. Một tệp
            tên <span className="font-mono">.pdf</span> mà bắt đầu bằng{' '}
            <span className="font-mono">&lt;script</span> là một script.
          </p>
          <p className="mt-1 text-meta text-muted">
            {accept.length === 0 ? (
              <>
                <span className="font-semibold">Đang để trống</span> — nghĩa là chấp nhận mọi
                kiểu hệ thống nhận diện được ({DETECTABLE.map((t) => t.label).join(', ')}). Vẫn
                là một tập đóng, không phải “bất cứ thứ gì”. Danh sách không có SVG, HTML hay tài
                liệu Office vì chúng mang được nội dung chạy được.
              </>
            ) : (
              <>Đã chọn {accept.length} kiểu. Tệp ngoài danh sách bị từ chối với mã 415.</>
            )}
          </p>
        </fieldset>

        <div className="grid content-start gap-3">
          <Field
            label="Dung lượng tối đa"
            hint={`Trần cứng của hệ thống là ${SYSTEM_MAX_MB} MB, áp dụng kể cả khi câu hỏi đặt cao hơn.`}
            error={fieldErrors['max_mb']}
          >
            <select
              className="input font-mono"
              value={maxMB}
              disabled={!canWrite}
              onChange={(e) => setMaxMB(Number(e.target.value))}
            >
              <option value={0}>Theo hệ thống ({SYSTEM_MAX_MB} MB)</option>
              {SIZE_CHOICES.map((mb) => (
                <option key={mb} value={mb}>
                  {mb} MB
                </option>
              ))}
            </select>
          </Field>

          <label className="flex items-start gap-2">
            <input
              type="checkbox"
              className="mt-0.5 shrink-0 accent-[#1a1a1a]"
              checked={required}
              disabled={!canWrite}
              onChange={(e) => setRequired(e.target.checked)}
            />
            <span className="text-meta">
              <span className="font-semibold">Bắt buộc phải nộp tệp</span>
              <span className="mt-0.5 block text-muted">
                Chỉ bắt buộc khi câu hỏi thực sự hiển thị theo luồng rẽ nhánh — server tự tính
                tập field hiển thị, không tin client.
              </span>
            </span>
          </label>
        </div>
      </div>

      {canWrite && (
        <div className="mt-3 border-t border-line pt-3">
          <div className="rounded border border-duesoon/50 bg-duesoon/5 px-2.5 py-2 text-meta">
            <span className="font-semibold text-duesoon">Lưu ý trước khi lưu: </span>
            API chưa có <span className="font-mono">GET /api/v1/forms/{'{id}'}/draft</span>, nên
            màn này đọc <span className="font-semibold">schema bản đang chạy</span>. Bấm lưu sẽ
            ghi đè bản nháp bằng schema đang chạy kèm thay đổi ở trên — nếu ai đó đang sửa dở bản
            nháp ở trình dựng biểu mẫu, phần sửa đó sẽ mất. Thay đổi chỉ có hiệu lực với người
            điền sau khi bạn publish version mới.
          </div>

          <div className="mt-2 flex items-center gap-2">
            <button
              type="button"
              className="btn-primary"
              disabled={!dirty || save.isPending}
              onClick={() => save.mutate()}
            >
              {save.isPending ? 'Đang lưu…' : 'Lưu vào bản nháp'}
            </button>
            {!dirty && <span className="text-meta text-muted">Chưa có thay đổi nào.</span>}
          </div>

          {save.isError && Object.keys(fieldErrors).length === 0 && (
            <div className="mt-2">
              <ErrorBanner error={save.error} />
            </div>
          )}
          {save.isSuccess && (
            <div className="mt-2" role="status">
              <p className="text-body text-ok">
                Đã lưu vào bản nháp. Cần publish version mới thì người điền mới thấy.
              </p>
              {save.data.issues?.length ? (
                <ul className="mt-1 list-disc pl-4 text-meta text-duesoon">
                  {save.data.issues.map((i, n) => (
                    <li key={`${i.code}-${n}`}>
                      {i.message}
                      {i.target ? ` (${i.target})` : ''}
                    </li>
                  ))}
                </ul>
              ) : null}
            </div>
          )}
        </div>
      )}

      {!canWrite && (
        <p className="mt-3 border-t border-line pt-2 text-meta text-muted">
          Chỉ xem: sửa cấu hình câu hỏi cần quyền <span className="font-mono">form.write</span>.
        </p>
      )}
    </Card>
  )
}

/** One received attachment, as GET /api/v1/forms/{id}/files returns it. */
interface Attachment {
  id: string
  field_id: string
  original_name: string
  content_type: string
  size_bytes: number
  status: string
  submission_id: string | null
  encrypted: boolean
  created_at: string
}

/** What POST /api/v1/files/{id}/download-url hands back. */
interface MintedLink {
  url: string
  filename: string
  expires_at: string
}

function bytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(0)} KB`
  return `${(n / 1024 / 1024).toFixed(1)} MB`
}

/**
 * The received-files half of the screen.
 *
 * The download link is minted per file, on click, rather than arriving with the
 * listing. Shipping a fetchable URL on every row would turn opening this tab
 * into a bulk read of every document on it, recorded as one audit line or none;
 * the mint endpoint writes a `submission.read_file` entry naming the operator
 * before it returns anything.
 */
function ReceivedFiles({ formId }: { formId: string }) {
  const files = useQuery({
    queryKey: ['form-files', formId],
    queryFn: () => api.get<{ data: Attachment[] }>(`/api/v1/forms/${formId}/files`),
  })

  // Not a redirect and not window.open: the response carries a filename, and the
  // browser's popup blocker eats a window opened from an async callback. An
  // anchor with `download` clicked synchronously after the await survives both.
  const mint = useMutation({
    mutationFn: (id: string) => api.post<MintedLink>(`/api/v1/files/${id}/download-url`),
    onSuccess: (link) => {
      const a = document.createElement('a')
      a.href = link.url
      a.download = link.filename
      a.rel = 'noopener'
      document.body.appendChild(a)
      a.click()
      a.remove()
    },
  })

  if (files.isPending) return <Card title="Tệp đã nhận"><Loading /></Card>
  if (files.error) {
    return (
      <Card title="Tệp đã nhận">
        <ErrorBanner error={files.error} retry={() => files.refetch()} />
      </Card>
    )
  }

  const rows = files.data?.data ?? []

  return (
    <Card title="Tệp đã nhận" aside="link tải ký hạn 10 phút">
      {rows.length === 0 ? (
        <Empty
          title="Chưa nhận tệp nào"
          hint="Biểu mẫu này chưa có tệp đính kèm nào được gửi lên."
        />
      ) : (
        <Table
          head={
            <>
              <Th>Tên tệp</Th>
              <Th>Câu hỏi</Th>
              <Th>Kiểu thật</Th>
              <Th className="text-right">Cỡ</Th>
              <Th>Lượt gửi</Th>
              <Th>Nhận lúc</Th>
              <Th>Trạng thái</Th>
              <Th />
            </>
          }
        >
          {rows.map((f) => (
            <Tr key={f.id}>
              <Td className="font-medium">{f.original_name}</Td>
              <Td className="font-mono text-meta text-muted">{f.field_id || '—'}</Td>
              {/* The type the server proved by reading the magic bytes, not the
                  extension the uploader chose. */}
              <Td className="font-mono text-meta text-muted">{f.content_type}</Td>
              <Td className="text-right tabular-nums">{bytes(f.size_bytes)}</Td>
              <Td className="font-mono text-meta">
                {f.submission_id ? f.submission_id.slice(0, 8) : '—'}
              </Td>
              <Td className="text-meta text-muted">{dateTime(f.created_at)}</Td>
              <Td>
                <StatusPill tone={f.status === 'bound' ? 'ok' : 'neutral'}>{f.status}</StatusPill>
              </Td>
              <Td className="text-right">
                <button
                  type="button"
                  className="btn py-0.5 text-meta"
                  disabled={mint.isPending}
                  onClick={() => mint.mutate(f.id)}
                >
                  Tải
                </button>
              </Td>
            </Tr>
          ))}
        </Table>
      )}

      {mint.error ? <div className="mt-2"><ErrorBanner error={mint.error} /></div> : null}

      <div className="mt-3 grid gap-1.5 text-meta text-muted">
        <p>
          Mỗi lần bấm “Tải” ghi một dòng <span className="font-mono">submission.read_file</span>{' '}
          vào nhật ký kèm tên người bấm. Link ký hết hạn sau 10 phút và là{' '}
          <span className="font-semibold">chứng chỉ mang theo</span> — ai cầm được link trong
          10 phút đó đều tải được, nên đừng dán nó vào chat.
        </p>
        <p>
          Tệp luôn trả về dưới dạng tải xuống, không bao giờ mở trực tiếp trong trình duyệt — một
          PDF hay ảnh mở inline từ cùng nguồn với phiên đăng nhập là một lỗ XSS.
        </p>
        <p>
          Tệp đã bị xoá theo yêu cầu chủ thể không còn trong danh sách này, nhưng dòng{' '}
          <span className="font-mono">dsr.erase</span> trong nhật ký vẫn chứng minh việc xoá đã
          diễn ra. Khoá mã hoá bị huỷ nên byte còn lại là không đọc được.
        </p>
      </div>
    </Card>
  )
}

function AttachmentsArePersonalData({ form }: { form: FormMeta | undefined }) {
  return (
    <Card title="Tệp đính kèm cũng là dữ liệu cá nhân">
      <div className="grid gap-2 text-body">
        <p>
          Một tệp người điền nộp lên không phải “tài liệu kèm theo” — nó{' '}
          <span className="font-semibold">là dữ liệu cá nhân</span>, và thường là loại nhạy cảm
          nhất trong cả biểu mẫu: ảnh CCCD, giấy tờ y tế, sao kê. Nó chịu đúng những nghĩa vụ mà
          câu trả lời dạng chữ phải chịu.
        </p>

        <ul className="grid gap-1.5 pl-1">
          <li className="flex gap-2">
            <span aria-hidden className="text-accent">
              ◆
            </span>
            <span>
              <span className="font-semibold">Chủ thể yêu cầu xoá thì tệp cũng bị xoá.</span> Xoá
              thật: object bị huỷ và khoá mã hoá của nó bị phá, nên bản sao còn sót ở tầng lưu
              trữ cũng không giải mã lại được. Sau đó URL tải trả về 404 giống hệt như tệp chưa
              từng tồn tại.
            </span>
          </li>
          <li className="flex gap-2">
            <span aria-hidden className="text-accent">
              ◆
            </span>
            <span>
              <span className="font-semibold">Tệp theo đúng chính sách lưu trữ của lượt gửi.</span>{' '}
              {form?.retention_days
                ? `Biểu mẫu này đặt hạn lưu ${form.retention_days} ngày (${form.retention_action || 'xoá'}), và tệp hết hạn cùng lúc với lượt gửi chứa nó — không có hạn lưu riêng cho tệp.`
                : 'Biểu mẫu này chưa đặt hạn lưu riêng nên áp dụng hạn của dự án. Tệp hết hạn cùng lúc với lượt gửi chứa nó — không có hạn lưu riêng cho tệp.'}
            </span>
          </li>
          <li className="flex gap-2">
            <span aria-hidden className="text-accent">
              ◆
            </span>
            <span>
              <span className="font-semibold">Tệp chưa gắn vào lượt gửi nào sẽ tự biến mất.</span>{' '}
              Tệp tải lên rồi bỏ dở nằm ở trạng thái{' '}
              <span className="font-mono text-meta">pending</span> và bị dọn sau 24h. Giữ lại
              là giữ dữ liệu cá nhân của một người thậm chí chưa bấm gửi.
            </span>
          </li>
          <li className="flex gap-2">
            <span aria-hidden className="text-accent">
              ◆
            </span>
            <span>
              <span className="font-semibold">Tệp không bao giờ đi qua webhook.</span> Payload
              webhook chỉ có id và metadata; bên nhận muốn lấy tệp thì phải gọi API và tự chịu
              kiểm tra quyền.
            </span>
          </li>
        </ul>

        <p className="flex items-center gap-2 text-meta text-muted">
          <StatusPill tone="accent">◆ nhắc lại</StatusPill>
          Nếu câu hỏi tệp thu thập giấy tờ tuỳ thân hay hồ sơ y tế, hãy đánh dấu field là{' '}
          <span className="font-semibold">nhạy cảm</span> ở trình dựng biểu mẫu. Dấu đó quyết
          định ai được xem và tệp có bị che trong bảng dữ liệu hay không.
        </p>
      </div>
    </Card>
  )
}
