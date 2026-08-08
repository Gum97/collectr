/**
 * 1d -- the "Tuân thủ" tab inside one form.
 *
 * Compliance lives next to the form it belongs to rather than in a separate
 * quarter of the app. The person who decides to add a health question is a
 * marketer building a campaign, and the consequences of that decision -- a new
 * lawful basis, a new consent version, a field that can only be erased by
 * destroying a key -- have to be visible at the moment they make it, not in a
 * screen they have no reason to open.
 */
import { useQuery } from '@tanstack/react-query'
import { Link, NavLink, useParams } from 'react-router'
import { api } from '../../lib/api'
import { retentionLabel, useProjects } from '../../lib/projects'
import { can, useMe } from '../../lib/session'
import {
  Card,
  Empty,
  ErrorBanner,
  Loading,
  PageHeader,
  SensitiveTag,
  StatusPill,
  date,
  num,
} from '../../components/ui'
import {
  activeDocument,
  publishedAt,
  shortHash,
  useConsentDocuments,
} from './ConsentDocuments'
import { legalBasisLabel, purposeRequired, usePurposes, type Purpose } from './Purposes'
import { isClosed, useDsrRequests } from './ComplianceCentre'

interface FormPurposeRef {
  code: string
  name?: string | null
  required?: boolean | null
}

interface SensitiveField {
  /** The API may name it either way; both are read. */
  id?: string
  field_id?: string
  label: string
  pii?: string | null
}

interface FormDetail {
  id: string
  project_id: string
  public_id: string
  title: string
  status: string
  live_version_id: string | null
  retention_days: number | null
  retention_action: string | null
  created_at: string
  /** The purposes and the sensitive markers live in the schema, because they are
   *  versioned with it: a published version carries the declaration that was in
   *  force when its submissions were collected. Read from the live version when
   *  there is one -- the draft describes what is being prepared, not what people
   *  are consenting to right now. */
  live_schema?: SchemaShape | null
  draft_schema?: SchemaShape | null
}

interface SchemaShape {
  fields?: Record<string, { label?: string; sensitive?: boolean; pii?: string | null }>
  consent?: { purposes?: FormPurposeRef[] | null; sensitive_notice_required?: boolean } | null
}

/** The version whose declaration is actually binding, or undefined when the form
 *  has never been published and has no draft either. */
function governing(f: FormDetail): SchemaShape | undefined {
  return f.live_schema ?? f.draft_schema ?? undefined
}

const FORM_TABS = [
  { id: 'build', label: 'Xây dựng' },
  { id: 'submissions', label: 'Dữ liệu' },
  { id: 'analytics', label: 'Phân tích' },
  { id: 'compliance', label: 'Tuân thủ' },
  { id: 'settings', label: 'Cài đặt' },
] as const

const FORM_STATUS: Record<string, string> = {
  live: 'đang chạy',
  draft: 'nháp',
  closed: 'đã đóng',
  archived: 'lưu trữ',
}

export function FormCompliance() {
  const { projectId, formId } = useParams()
  const me = useMe()

  const form = useQuery({
    queryKey: ['form', formId],
    queryFn: () => api.get<FormDetail>(`/api/v1/forms/${formId}`),
    enabled: Boolean(formId),
  })

  if (form.isPending) return <Loading label="Đang tải biểu mẫu…" />
  if (form.isError) {
    return (
      <div className="p-6">
        <ErrorBanner error={form.error} retry={() => void form.refetch()} />
      </div>
    )
  }

  const detail = form.data

  return (
    <div className="p-6">
      <PageHeader
        title={detail.title}
        meta={
          <>
            {detail.public_id} · {FORM_STATUS[detail.status] ?? detail.status}
            {detail.live_version_id ? '' : ' · chưa publish'}
          </>
        }
      />

      <nav
        aria-label="Khu vực biểu mẫu"
        className="mb-4 flex flex-wrap gap-3.5 border-b border-line pb-1.5 text-meta font-semibold"
      >
        {FORM_TABS.map((t) => (
          <NavLink
            key={t.id}
            to={`/p/${projectId}/forms/${formId}/${t.id}`}
            className={({ isActive }) =>
              isActive
                ? 'border-b-2 border-line pb-1 text-ink'
                : 'border-b-2 border-transparent pb-1 text-muted hover:text-ink'
            }
          >
            {t.label}
          </NavLink>
        ))}
      </nav>

      <div className="flex flex-col gap-2.5">
        <PurposesCard form={detail} />
        <ConsentDocumentCard canManage={can(me.data, 'consent.manage')} />
        <SensitiveFieldsCard fields={sensitiveFieldsOf(detail)} />

        <div className="grid gap-2.5 sm:grid-cols-2">
          <RetentionTile form={detail} />
          <FormRequestsTile formId={detail.id} />
        </div>
      </div>

      <p className="mt-3 id-chip">
        Tuân thủ sống cạnh biểu mẫu nó thuộc về, không phải một khu riêng.
      </p>
    </div>
  )
}

/** Purposes declared by this form, each with the lawful basis it rests on. */
function PurposesCard({ form }: { form: FormDetail }) {
  const purposes = usePurposes()
  const schema = governing(form)
  // null means "the schema could not be read at all"; an empty array means the
  // schema was read and declares nothing. They lead to opposite conclusions.
  const refs = schema ? (schema.consent?.purposes ?? []) : null
  const byCode = new Map((purposes.data ?? []).map((p) => [p.code, p]))

  return (
    <Card
      title="Mục đích xử lý & căn cứ pháp lý"
      aside={refs ? <span className="text-accent">{num(refs.length)} mục đích</span> : undefined}
    >
      {purposes.isPending && <Loading />}
      {purposes.isError && <ErrorBanner error={purposes.error} retry={() => void purposes.refetch()} />}

      {!refs && !purposes.isPending && (
        // Never "biểu mẫu này không có mục đích nào": that would be a legal claim
        // made on the strength of a field the API has not sent.
        <Empty
          title="Chưa đọc được mục đích của biểu mẫu này"
          hint={
            <>
              Biểu mẫu này chưa có version nào đọc được — chưa publish và cũng chưa có bản nháp.
              Danh sách mục đích cấp tổ chức nằm ở{' '}
              <Link to="/compliance?tab=purposes" className="underline">
                Trung tâm tuân thủ → Mục đích
              </Link>
              .
            </>
          }
        />
      )}

      {refs && refs.length === 0 && (
        <Empty
          title="Biểu mẫu chưa khai báo mục đích nào"
          hint="Biểu mẫu thu thập dữ liệu cá nhân phải nêu ít nhất một mục đích trước khi publish."
        />
      )}

      {refs && refs.length > 0 && (
        <ul className="flex flex-col">
          {refs.map((ref) => {
            const known = byCode.get(ref.code)
            return (
              <li
                key={ref.code}
                className="flex items-center justify-between gap-3 border-t border-line py-2 first:border-t-0 first:pt-0"
              >
                <div className="min-w-0">
                  <div className="text-meta font-semibold">
                    {known?.name ?? ref.name ?? ref.code}
                  </div>
                  <div className="id-chip">{purposeLine(ref, known)}</div>
                </div>
                {known ? (
                  <StatusPill tone={known.legal_basis === 'consent' ? 'accent' : 'neutral'}>
                    {legalBasisLabel(known.legal_basis)}
                  </StatusPill>
                ) : (
                  // A purpose code the form uses but the organisation never
                  // declared has no lawful basis on record. That is a finding.
                  <StatusPill tone="overdue">chưa khai báo căn cứ</StatusPill>
                )}
              </li>
            )
          })}
        </ul>
      )}
    </Card>
  )
}

function purposeLine(ref: FormPurposeRef, known: Purpose | undefined): string {
  const required = ref.required ?? (known ? purposeRequired(known) : null)
  return [
    ref.code,
    required === null ? 'chưa rõ bắt buộc' : required ? 'bắt buộc' : 'tùy chọn',
    known ? retentionLabel(known.retention_days ?? null) : 'chưa có hạn lưu riêng',
  ].join(' · ')
}

/**
 * The consent text respondents are agreeing to right now.
 *
 * The server resolves the active document per tenant and kind, so this is the
 * organisation's current consent text -- not a per-form copy. Publishing a new
 * version is the only way to change it.
 */
function ConsentDocumentCard({ canManage }: { canManage: boolean }) {
  const docs = useConsentDocuments()
  const active = activeDocument(docs.data)

  return (
    <Card title="Văn bản đồng ý đang dùng">
      {docs.isPending && <Loading />}
      {docs.isError && <ErrorBanner error={docs.error} retry={() => void docs.refetch()} />}

      {docs.data && !active && (
        <Empty
          title="Chưa có văn bản đồng ý nào được publish"
          hint="Biểu mẫu thu thập dữ liệu cá nhân cần một văn bản đã publish để bản ghi đồng ý có chỗ trỏ về."
        />
      )}

      {active && (
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="min-w-0">
            <div className="font-mono text-meta">
              {active.id} · v{active.version_no} · publish {date(publishedAt(active))}
            </div>
            <div className="mt-0.5 flex items-center gap-1.5">
              <span className="id-chip">{shortHash(active.content_hash)}</span>
              <StatusPill>bất biến</StatusPill>
            </div>
          </div>
          <div className="flex shrink-0 gap-1.5">
            <a
              className="btn"
              href={active.permalink ?? `/p/${active.id}`}
              target="_blank"
              rel="noreferrer"
            >
              Xem
            </a>
            {canManage ? (
              <Link className="btn-primary" to="/compliance?tab=documents&new=1">
                Tạo v{active.version_no + 1}
              </Link>
            ) : (
              <span className="id-chip self-center">cần quyền consent.manage để tạo version</span>
            )}
          </div>
        </div>
      )}
    </Card>
  )
}

/** The sensitive questions of the governing version, or undefined when no
 *  version could be read -- which is not the same as "none". */
function sensitiveFieldsOf(f: FormDetail): SensitiveField[] | undefined {
  const schema = governing(f)
  if (!schema?.fields) return undefined
  return Object.entries(schema.fields)
    .filter(([, v]) => v.sensitive)
    .map(([id, v]) => ({ id, label: v.label ?? id, pii: v.pii ?? null }))
}

/** Sensitive fields, with what being sensitive actually costs. */
function SensitiveFieldsCard({ fields }: { fields: SensitiveField[] | null | undefined }) {
  // Three states, and they must not collapse into two. "No sensitive fields" and
  // "we could not find out" lead to opposite decisions about who may read the
  // responses.
  if (fields === null || fields === undefined) {
    return (
      <section className="rounded border border-dashed border-duesoon bg-duesoon/5 p-3">
        <h2 className="text-meta font-semibold text-duesoon">Chưa xác định được field nhạy cảm</h2>
        <p className="mt-1 text-body">
          Chưa đọc được version nào của biểu mẫu, nên màn hình này không khẳng định được nó có
          hay không có dữ liệu nhạy cảm. Đừng coi đây là “không có”.
        </p>
      </section>
    )
  }

  if (fields.length === 0) {
    return (
      <Card title="Field nhạy cảm">
        <p className="text-body text-muted">
          Biểu mẫu này không thu thập dữ liệu cá nhân nhạy cảm. Thêm một field nhạy cảm là thay đổi
          breaking: phải cập nhật văn bản đồng ý và publish version mới.
        </p>
      </Card>
    )
  }

  return (
    <section className="rounded border border-accent bg-accent/5 p-3">
      <div className="flex items-baseline justify-between gap-2">
        <h2 className="text-meta font-semibold text-legal">Field nhạy cảm ({num(fields.length)})</h2>
        <SensitiveTag />
      </div>
      <ul className="mt-1.5 flex flex-col gap-1">
        {fields.map((f) => {
          const id = f.field_id ?? f.id ?? ''
          return (
            <li key={id || f.label} className="text-body">
              <span className="font-mono text-meta">{id}</span>{' '}
              <span className="font-semibold">“{f.label}”</span> — mã hóa bằng khóa riêng của chủ
              thể, chỉ vai trò có{' '}
              <span className="font-mono text-meta">submission.read_sensitive</span> xem được, xóa
              bằng crypto-shred.
            </li>
          )
        })}
      </ul>
      <p className="mt-1.5 text-meta text-accent/90">
        Crypto-shred là xóa khóa, không phải xóa dòng: sau khi xóa, ciphertext trong mọi bản backup
        đã tạo trước đó cũng không giải mã được nữa — và không khôi phục lại được.
      </p>
    </section>
  )
}

function RetentionTile({ form }: { form: FormDetail }) {
  const projects = useProjects()
  const project = projects.data?.find((p) => p.id === form.project_id)
  const inherited = form.retention_days === null
  const days = form.retention_days ?? project?.default_retention_days ?? null

  const action =
    form.retention_action === 'anonymize'
      ? 'ẩn danh'
      : form.retention_action === 'delete'
        ? 'xóa'
        : null

  return (
    <div className="rounded border border-dashed border-line p-2.5">
      <div className="font-mono text-[8px] tracking-caps text-faint">CHÍNH SÁCH LƯU TRỮ</div>
      <div className="text-body font-semibold">
        {days === null ? 'chưa đặt hạn lưu' : `${num(days)} ngày${action ? ` → ${action}` : ''}`}
      </div>
      <div className="id-chip mt-0.5">
        {days === null
          ? 'dữ liệu không có hạn tự xóa'
          : inherited
            ? `kế thừa từ dự án · ${retentionLabel(days)}`
            : `riêng của biểu mẫu · ${retentionLabel(days)}`}
      </div>
      <p className="mt-1 text-meta text-muted">
        Hạn xóa tính tại thời điểm gửi; đổi chính sách không hồi tố lên bản ghi cũ.
      </p>
    </div>
  )
}

/** Open subject requests touching this form. */
function FormRequestsTile({ formId }: { formId: string }) {
  const me = useMe()
  const mayHandle = can(me.data, 'dsr.handle')
  const requests = useDsrRequests()

  const rows = requests.data?.data ?? []
  const open = rows.filter((r) => !isClosed(r))
  // Whether the queue can be narrowed to one form depends on the API carrying a
  // form id. Without it, the honest answer is "not measured" -- not zero.
  const scopeable = open.length === 0 || open.some((r) => r.form_id)
  const mine = open.filter((r) => r.form_id === formId)
  const overdue = mine.filter((r) => r.overdue)

  let value = '—'
  let hint = 'cần quyền dsr.handle'
  if (mayHandle) {
    if (requests.isPending) {
      value = '…'
      hint = 'đang tải'
    } else if (!scopeable) {
      hint = 'API chưa lọc được yêu cầu theo biểu mẫu'
    } else {
      value = `${num(mine.length)} mở · ${num(overdue.length)} quá hạn`
      hint = `toàn tổ chức: ${num(open.length)} yêu cầu đang mở`
    }
  }

  return (
    <div
      className={`rounded border border-dashed p-2.5 ${
        overdue.length > 0 ? 'border-overdue text-overdue' : 'border-faint'
      }`}
    >
      <div className="font-mono text-[8px] tracking-caps opacity-70">
        YÊU CẦU CHỦ THỂ (FORM NÀY)
      </div>
      <div className="text-body font-semibold">{value}</div>
      <div className="id-chip mt-0.5">{hint}</div>
      {mayHandle && (
        <Link to="/compliance?tab=requests" className="mt-1 inline-block text-meta underline">
          Mở hàng đợi yêu cầu
        </Link>
      )}
    </div>
  )
}
