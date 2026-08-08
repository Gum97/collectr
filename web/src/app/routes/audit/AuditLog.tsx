/**
 * Screen 1m -- the append-only audit trail.
 *
 * There is no delete control and no edit control anywhere on this screen, and
 * that is the design rather than an omission: the API offers neither, the
 * application's database role holds only INSERT and SELECT on audit.entries, and
 * an interface that appeared to offer them would misrepresent what the system
 * can do.
 *
 * Each row answers the five questions an investigation asks: who, what, on which
 * object, when, and from which network. The network is a /24 prefix, not an
 * address -- enough to tell an office from a datacentre without storing the
 * thing that identifies a person.
 */
import { useState } from 'react'
import { useInfiniteQuery } from '@tanstack/react-query'
import { api, RequestFailed } from '../../lib/api'
import {
  Card,
  Empty,
  ErrorBanner,
  Loading,
  PageHeader,
  StatusPill,
  date,
  num,
} from '../../components/ui'
import { can, useMe } from '../../lib/session'
import { VerifyButton, VerifyPanel, useChainVerify } from './ChainVerify'

/**
 * The actor as it comes back from the API.
 *
 * The stored column is written by encoding/json over contracts.AuditActor, which
 * carries no struct tags, so the JSON in the database has keys `Type`, `ID` and
 * `IPPrefix`. Whether the list endpoint re-maps them to snake_case is up to
 * whoever writes it; both spellings are read here so this screen does not break
 * on that decision. `email` and `role` are not stored on the entry at all -- they
 * would have to be joined from the user directory -- so they are optional and
 * the row falls back to the raw id.
 */
interface AuditActor {
  type?: string
  id?: string
  ip_prefix?: string
  Type?: string
  ID?: string
  IPPrefix?: string
  email?: string
  role?: string
}

interface AuditRow {
  seq: number
  occurred_at: string
  action: string
  /** The endpoint projects the actor into flat fields; see actorOf. */
  actor_type?: string
  actor_id?: string
  actor_email?: string
  actor_role?: string
  ip_prefix?: string
  /** The older nested shape, still read so a mixed deployment does not crash. */
  actor?: AuditActor
  target?: Record<string, unknown> | null
  payload?: Record<string, unknown> | null
  hash?: string
}

/**
 * Reads the actor whichever shape it arrives in.
 *
 * The screen was written against a nested `actor` object before the list
 * endpoint existed, and the endpoint that got built projects flat fields
 * instead. Reading `row.actor.email` against the flat shape threw on the first
 * row and left the page blank -- no error message, just nothing. Tolerating both
 * costs a few lines and removes a whole class of white screen.
 */
function actorOf(row: AuditRow): { who: string; role?: string; ip?: string } {
  const nested = row.actor
  return {
    who:
      row.actor_email ||
      row.actor_id ||
      nested?.email ||
      nested?.id ||
      nested?.ID ||
      'không rõ',
    role: row.actor_role || nested?.role,
    ip: row.ip_prefix || nested?.ip_prefix || nested?.IPPrefix,
  }
}

interface AuditPage {
  data: AuditRow[]
  next_cursor?: string | null
}

/** The actions the writer emits, straight from internal/modules/audit/chain.go.
 *  Labels are for reading; the value sent to the API is the raw action string. */
const ACTIONS: { value: string; label: string }[] = [
  { value: '', label: 'Mọi hành động' },
  { value: 'submission.read_bulk', label: 'Truy cập hàng loạt' },
  { value: 'submission.sensitive_revealed', label: 'Mở field nhạy cảm' },
  { value: 'submission.created', label: 'Tạo bản ghi' },
  { value: 'submission.updated', label: 'Sửa bản ghi' },
  { value: 'submission.erased', label: 'Xoá bản ghi' },
  { value: 'consent.granted', label: 'Đồng ý' },
  { value: 'consent.withdrawn', label: 'Rút đồng ý' },
  { value: 'dsr.received', label: 'Nhận yêu cầu chủ thể' },
  { value: 'dsr.fulfilled', label: 'Thực hiện yêu cầu chủ thể' },
  { value: 'form.published', label: 'Publish biểu mẫu' },
  { value: 'retention.purge', label: 'Dọn theo hạn lưu' },
]

export function AuditLog() {
  const me = useMe()
  const allowed = can(me.data, 'audit.read')

  const [actorInput, setActorInput] = useState('')
  const [actor, setActor] = useState('')
  const [action, setAction] = useState('')
  const verify = useChainVerify()

  const q = useInfiniteQuery({
    queryKey: ['audit', actor, action],
    initialPageParam: '',
    queryFn: ({ pageParam }) => {
      const p = new URLSearchParams()
      if (pageParam) p.set('cursor', pageParam)
      if (actor) p.set('actor', actor)
      if (action) p.set('action', action)
      return api.get<AuditPage>(`/api/v1/audit?${p.toString()}`)
    },
    getNextPageParam: (last) => last.next_cursor || undefined,
    enabled: allowed,
  })

  const rows = q.data?.pages.flatMap((p) => p.data) ?? []
  const brokenAt = verify.result && !verify.result.valid ? verify.result.broken_at : undefined
  const filtered = Boolean(actor || action)

  if (me.isPending) return <Loading />

  if (!allowed) {
    // Read from the capabilities the API hands out, not re-derived from a role
    // name here. The people this log records should not be the ones deciding who
    // may read it.
    return (
      <div className="p-6">
        <PageHeader title="Nhật ký audit" />
        <Empty
          title="Bạn không có quyền đọc nhật ký audit"
          hint="Đọc nhật ký là một quyền riêng (audit.read), tách khỏi quyền quản trị: nó ghi lại chính hành động của những người quản trị. Liên hệ DPO hoặc org admin nếu bạn cần quyền này."
        />
      </div>
    )
  }

  return (
    <div className="p-6">
      <PageHeader
        title="Nhật ký audit"
        meta="chỉ ghi thêm · chuỗi hash · không sửa được kể cả bởi owner"
        actions={<VerifyButton verify={verify} />}
      />

      <div className="mb-4">
        <VerifyPanel verify={verify} />
      </div>

      <form
        className="mb-4 flex flex-wrap items-end gap-2 rounded border border-line bg-panel px-3 py-2"
        onSubmit={(e) => {
          e.preventDefault()
          setActor(actorInput.trim())
        }}
      >
        <div className="flex min-w-[200px] flex-1 flex-col gap-1">
          <label htmlFor="audit-actor" className="text-meta font-semibold">
            Người thực hiện
          </label>
          <input
            id="audit-actor"
            className="input"
            placeholder="email hoặc id người dùng…"
            value={actorInput}
            onChange={(e) => setActorInput(e.target.value)}
          />
        </div>
        <div className="flex flex-col gap-1">
          <label htmlFor="audit-action" className="text-meta font-semibold">
            Hành động
          </label>
          <select
            id="audit-action"
            className="input w-auto"
            value={action}
            onChange={(e) => setAction(e.target.value)}
          >
            {ACTIONS.map((a) => (
              <option key={a.value || 'all'} value={a.value}>
                {a.label}
              </option>
            ))}
          </select>
        </div>
        <button type="submit" className="btn">
          Lọc
        </button>
        {filtered && (
          <button
            type="button"
            className="btn"
            onClick={() => {
              setActorInput('')
              setActor('')
              setAction('')
            }}
          >
            Bỏ lọc
          </button>
        )}
      </form>

      {q.isPending && <Loading label="Đang đọc nhật ký…" />}

      {q.isError && (
        <div className="flex flex-col gap-2">
          <ErrorBanner error={q.error} retry={() => void q.refetch()} />
          {!(q.error instanceof RequestFailed) && (
            <p className="text-meta text-muted">
              Nếu lỗi lặp lại: bản triển khai này có thể chưa bật{' '}
              <code className="font-mono">GET /api/v1/audit</code>. Nhật ký vẫn đang được ghi và nút
              “Kiểm toàn vẹn” ở trên vẫn chạy được — chỉ là chưa đọc danh sách ra được.
            </p>
          )}
        </div>
      )}

      {q.data && rows.length === 0 && (
        <Empty
          title={filtered ? 'Không có bản ghi nào khớp bộ lọc' : 'Nhật ký chưa có bản ghi nào'}
          hint={
            filtered
              ? 'Bộ lọc đang thu hẹp theo người thực hiện và loại hành động. Bỏ lọc để xem toàn bộ.'
              : 'Chưa có hành động nào cần ghi vết được thực hiện trong tổ chức này. Bản ghi đầu tiên sẽ xuất hiện khi ai đó publish biểu mẫu, xuất dữ liệu hoặc mở một field nhạy cảm.'
          }
        />
      )}

      {rows.length > 0 && (
        <Card title={`${num(rows.length)} bản ghi gần nhất`} aside="mới nhất trước">
          <ol className="flex flex-col">
            {rows.map((r, i) => {
              const prev = i > 0 ? rows[i - 1] : undefined
              const day = date(r.occurred_at)
              const newDay = !prev || date(prev.occurred_at) !== day
              return (
                <li key={`${r.seq}`}>
                  {newDay && (
                    <div className="border-b border-line py-1 font-mono text-meta tracking-caps text-faint">
                      {day.toUpperCase()}
                    </div>
                  )}
                  <Entry row={r} suspect={brokenAt !== undefined && r.seq >= brokenAt} />
                </li>
              )
            })}
          </ol>

          {q.hasNextPage && (
            <button
              type="button"
              className="btn mt-3"
              onClick={() => void q.fetchNextPage()}
              disabled={q.isFetchingNextPage}
            >
              {q.isFetchingNextPage ? 'Đang tải…' : 'Tải thêm'}
            </button>
          )}

          <p className="mt-3 border-t border-dashed border-line pt-2 text-meta text-muted">
            Không có nút xoá hay sửa ở đâu trong màn hình này — đó là điểm chính. Mỗi bản ghi mang
            hash của bản ghi liền trước, nên sửa một dòng sẽ làm hỏng toàn bộ phần sau và lộ ra ở
            phần kiểm toàn vẹn phía trên.
          </p>
        </Card>
      )}
    </div>
  )
}

function Entry({ row, suspect }: { row: AuditRow; suspect: boolean }) {
  const tag = tagOf(row.action)
  const { who, role: actorRole, ip: actorIP } = actorOf(row)
  const kind = row.actor_type || row.actor?.type || row.actor?.Type || ''
  const net = actorIP || ''
  const reason = readString(row.payload, 'reason')

  return (
    <div
      className={`flex gap-3 border-b border-line py-2 last:border-0 ${
        suspect ? 'border-l-1.5 border-l-overdue bg-overdue/5 pl-2' : ''
      }`}
    >
      <div className="w-16 shrink-0 font-mono text-meta text-muted">
        {new Date(row.occurred_at).toLocaleTimeString('vi-VN')}
        <div className="text-faint">#{num(row.seq)}</div>
      </div>

      <div className="min-w-0 flex-1">
        <div className="text-body font-semibold">
          <span className="font-mono">{row.action}</span>
          {summariseTarget(row.target) && (
            <span className="font-normal text-muted"> · {summariseTarget(row.target)}</span>
          )}
        </div>
        <div className="id-chip">
          {who}
          {kind && ` · ${kind}`}
          {actorRole && ` · ${actorRole}`}
          {reason && ` · lý do: “${reason}”`}
        </div>
        <div className="id-chip">
          {/* A /24, deliberately: enough to tell an office from a datacentre,
              not enough to point at a person. */}
          {net ? `từ dải ${net}` : 'không ghi được dải mạng'}
        </div>
        {suspect && (
          <p className="mt-1 text-meta font-semibold text-overdue">
            Nằm sau điểm đứt của chuỗi hash — bản ghi này không còn tự chứng minh được.
          </p>
        )}
        {(row.target || row.payload) && (
          <details className="mt-1">
            <summary className="cursor-pointer font-mono text-meta text-muted">chi tiết</summary>
            <pre className="mt-1 overflow-x-auto rounded border border-line bg-panel p-2 font-mono text-meta">
              {JSON.stringify({ target: row.target, payload: row.payload }, null, 2)}
            </pre>
          </details>
        )}
      </div>

      <div className="shrink-0">
        {tag.tone === 'accent' ? (
          <StatusPill tone="accent">{tag.label}</StatusPill>
        ) : (
          <span className="id-chip">{tag.label}</span>
        )}
      </div>
    </div>
  )
}

/**
 * The module an action belongs to, and whether it is one of the two that deserve
 * a mark of their own.
 *
 * Bulk access and revealing a sensitive field are singled out because they are
 * the two lines an investigation looks for first. This is presentation only --
 * no permission is decided here.
 */
function tagOf(action: string): { label: string; tone: 'accent' | 'neutral' } {
  if (action === 'submission.read_bulk') return { label: 'hàng loạt', tone: 'accent' }
  if (action === 'submission.sensitive_revealed') return { label: 'nhạy cảm', tone: 'accent' }
  const module = action.split('.')[0] ?? action
  return { label: module, tone: 'neutral' }
}

/** A short "on what" for the row heading: the first couple of target keys, so
 *  the common case reads without opening the detail block. */
function summariseTarget(target: Record<string, unknown> | null | undefined): string {
  if (!target) return ''
  return Object.entries(target)
    .slice(0, 2)
    .map(([k, v]) => `${k}=${scalar(v)}`)
    .join(' · ')
}

function readString(obj: Record<string, unknown> | null | undefined, key: string): string {
  const v = obj?.[key]
  return typeof v === 'string' ? v : ''
}

function scalar(v: unknown): string {
  if (v === null || v === undefined) return '—'
  if (typeof v === 'string' || typeof v === 'number' || typeof v === 'boolean') return String(v)
  return '…'
}
