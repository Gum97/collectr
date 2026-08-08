import { useState } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { Link, useNavigate, useSearchParams } from 'react-router'
import { api, RequestFailed } from '../../lib/api'
import { Loading, StatusPill, deadline } from '../../components/ui'
import { checkPassword } from './password'
import { PasswordMeter } from './ResetPassword'
import {
  CAPABILITIES,
  NOTABLE_DENIALS,
  effectiveCapabilities,
  requiresMFA,
  roleLabel,
  type Capability,
} from '../members/roles'

/**
 * What the acceptance page needs to show.
 *
 * `email`, `org_role` and `needs_signup` are what the server sends today.
 * Everything below them is optional and rendered only when present: the name of
 * the organisation, who is inviting, which projects, and -- most importantly --
 * what the role actually permits.
 *
 * When the server sends `capabilities`, that is what is shown. When it does not,
 * the screen falls back to the mirrored role table in `routes/members/roles.ts`
 * and says so on the page. The fallback is not an access decision -- nothing here
 * grants anything -- it is the only way to answer the one question the API cannot
 * answer for this reader: what a role means to somebody who does not hold it yet
 * and therefore has no `me.capabilities` to consult.
 */
interface InvitationPreview {
  email: string
  org_role: string
  needs_signup: boolean

  org_name?: string
  invited_by_email?: string
  role_label?: string
  expires_at?: string
  /** Whether accepting will force MFA enrolment at first sign-in. */
  mfa_required?: boolean
  project_grants?: { project_id?: string; project_name?: string; role_label?: string; role: string }[]
  /** Every capability the role touches, granted or not. Both halves matter: the
   *  ones withheld are what tell somebody this is an oversight role and not an
   *  operational one. */
  capabilities?: { code: string; granted: boolean }[]
}

/** Screen 3c: read what you are being given, then set a password. */
export function AcceptInvite() {
  const [params] = useSearchParams()
  const token = params.get('token') ?? ''
  const nav = useNavigate()

  const [name, setName] = useState('')
  const [password, setPassword] = useState('')
  const [reveal, setReveal] = useState(false)

  const preview = useQuery({
    queryKey: ['invitation', token],
    queryFn: () => api.get<InvitationPreview>(`/api/auth/invitations/${encodeURIComponent(token)}`),
    enabled: token !== '',
    retry: false,
    staleTime: Infinity,
  })

  const accept = useMutation({
    mutationFn: (p: InvitationPreview) =>
      // Only the three fields the endpoint declares: it rejects unknown ones.
      api.post<{ message?: string }>('/api/auth/invitations/accept', {
        token,
        name: name.trim(),
        password: p.needs_signup ? password : '',
      }),
    onSuccess: () => nav('/login', { replace: true }),
  })

  if (token === '') return <Dead kind="missing" />
  if (preview.isPending) {
    return (
      <Frame title="Lời mời tham gia">
        <Loading label="Đang mở lời mời…" />
      </Frame>
    )
  }
  if (preview.isError) return <Dead kind={kindOf(preview.error)} error={preview.error} />

  const invite = preview.data
  // The server ignores the password when the address already has an account, so
  // asking for one would be theatre -- and worse, it would suggest an invitation
  // can overwrite the credentials of an account somebody already holds.
  const check = checkPassword(password, { email: invite.email })
  const ready = !invite.needs_signup || check.ok
  const fieldErrors = accept.error instanceof RequestFailed ? accept.error.fields : {}
  const left = invite.expires_at ? deadline(invite.expires_at) : null
  const role = invite.role_label ?? roleLabel(invite.org_role)
  // The server does not say yet; the mirrored table does, and this only decides
  // which sentence to print -- the enrolment itself is forced by the API at
  // sign-in either way.
  const mfaForced = invite.mfa_required ?? requiresMFA(invite.org_role)

  return (
    <Frame title={`Bạn được mời vào ${invite.org_name ?? 'một tổ chức trên Collectr'}`} wide>
      <p className="text-body leading-relaxed">
        {invite.invited_by_email ? (
          <span className="font-mono text-meta">{invite.invited_by_email}</span>
        ) : (
          'Quản trị viên của tổ chức'
        )}{' '}
        mời <span className="font-mono text-meta">{invite.email}</span> tham gia với vai trò{' '}
        <b>{role}</b>
        {invite.project_grants && invite.project_grants.length > 0 && (
          <>
            {' '}
            trong dự án{' '}
            {invite.project_grants.map((g, i) => (
              <span key={g.project_id ?? g.project_name ?? i}>
                {i > 0 && ', '}
                <b>{g.project_name ?? g.project_id}</b>
                {g.role_label || g.role ? ` (${g.role_label ?? g.role})` : ''}
              </span>
            ))}
          </>
        )}
        .
      </p>

      <Capabilities invite={invite} />

      <form
        onSubmit={(e) => {
          e.preventDefault()
          accept.mutate(invite)
        }}
        className="mt-4 flex flex-col gap-3"
      >
        {invite.needs_signup ? (
          <>
            <label className="flex flex-col gap-1">
              <span className="text-meta font-semibold">Họ tên</span>
              <input
                required
                autoFocus
                value={name}
                onChange={(e) => setName(e.target.value)}
                className="input"
              />
              <span className="text-meta text-muted">
                Tên này xuất hiện cạnh mọi thao tác của bạn trong nhật ký audit.
              </span>
            </label>

            <div className="flex flex-col gap-1">
              <div className="flex items-baseline justify-between">
                <label htmlFor="invite-password" className="text-meta font-semibold">
                  Đặt mật khẩu
                </label>
                <button
                  type="button"
                  onClick={() => setReveal((v) => !v)}
                  className="text-meta text-accent hover:text-accent-dark"
                >
                  {reveal ? 'Ẩn' : 'Hiện'}
                </button>
              </div>
              <input
                id="invite-password"
                type={reveal ? 'text' : 'password'}
                autoComplete="new-password"
                required
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                className="input font-mono"
              />
              {fieldErrors.password && (
                <span role="alert" className="text-meta text-overdue">
                  {fieldErrors.password}
                </span>
              )}
            </div>

            <PasswordMeter check={check} />
          </>
        ) : (
          <p className="rounded border border-dashed border-line bg-panel px-3 py-2 text-meta leading-relaxed text-muted">
            Địa chỉ này đã có tài khoản Collectr. Bạn giữ nguyên mật khẩu đang dùng — lời mời không
            đổi được mật khẩu của một tài khoản đã tồn tại.
          </p>
        )}

        <p
          className={`rounded border px-3 py-2 text-meta leading-relaxed ${
            mfaForced ? 'border-accent bg-accent/5' : 'border-dashed border-faint bg-panel text-muted'
          }`}
        >
          {mfaForced
            ? 'Vai trò này chạm tới dữ liệu cá nhân trên toàn tổ chức, nên bước tiếp theo là bật xác thực 2 lớp — không bỏ qua được.'
            : 'Nếu vai trò của bạn yêu cầu, hệ thống sẽ bắt bật xác thực 2 lớp ngay ở lần đăng nhập đầu tiên.'}
        </p>

        {accept.isError && Object.keys(fieldErrors).length === 0 && (
          <p
            role="alert"
            className="rounded border border-overdue/40 bg-overdue/5 px-3 py-2 text-body text-overdue"
          >
            {acceptErrorText(accept.error)}
          </p>
        )}

        <button type="submit" disabled={!ready || accept.isPending} className="btn-primary">
          {accept.isPending ? 'Đang tham gia…' : 'Nhận lời mời & tiếp tục'}
        </button>
      </form>

      <p className="mt-2 text-center text-meta text-muted">
        {left
          ? left.overdue
            ? `Lời mời đã ${left.text}`
            : `Lời mời hết hạn — ${left.text}`
          : 'Lời mời có hiệu lực trong 7 ngày kể từ khi được gửi.'}
      </p>
    </Frame>
  )
}

/**
 * What the role permits, as the API describes it.
 *
 * Withheld capabilities are listed alongside the granted ones because that
 * contrast is the whole point of the screen: "reads submissions but cannot
 * export them" is a materially different job from "reads submissions".
 */
function Capabilities({ invite }: { invite: InvitationPreview }) {
  const projectRoles = (invite.project_grants ?? []).map((g) => g.role)
  const fromServer = invite.capabilities ?? []

  // Server first. The mirrored table is only consulted when the response says
  // nothing, and the reader is told which of the two they are looking at.
  const derived = fromServer.length === 0
  const granted: string[] = derived
    ? effectiveCapabilities(invite.org_role, projectRoles)
    : fromServer.filter((c) => c.granted).map((c) => c.code)

  const withheld: string[] = derived
    ? [...new Set([invite.org_role, ...projectRoles].flatMap(notableDenials))].filter(
        (c) => !granted.includes(c),
      )
    : fromServer.filter((c) => !c.granted).map((c) => c.code)

  if (granted.length === 0 && withheld.length === 0) {
    return (
      <div className="mt-3 rounded border border-dashed border-line bg-panel px-3 py-3">
        <p className="text-meta font-semibold">Vai trò này chưa cho quyền nào ở cấp tổ chức</p>
        <p className="mt-1 text-meta leading-relaxed text-muted">
          Bạn chỉ thấy được những dự án mà quản trị viên thêm bạn vào. Mặc định là không thấy gì —
          không phải thấy tất cả rồi bị trừ dần.
        </p>
      </div>
    )
  }

  return (
    <div className="mt-3 rounded border border-line px-3 py-3">
      <p className="text-meta font-semibold text-muted">Vai trò này cho bạn</p>
      <ul className="mt-1.5 flex flex-col gap-1">
        {granted.map((c) => (
          <CapabilityRow key={c} code={c} granted />
        ))}
      </ul>

      {withheld.length > 0 && (
        <ul className="mt-2 flex flex-col gap-1 border-t border-line pt-2">
          {withheld.map((c) => (
            <CapabilityRow key={c} code={c} granted={false} />
          ))}
        </ul>
      )}

      {derived && (
        <p className="mt-2 border-t border-dashed border-line pt-2 text-meta leading-relaxed text-muted">
          Danh sách này mô tả vai trò <b>{roleLabel(invite.org_role)}</b> theo bảng quyền của ứng
          dụng. Quyền thực tế do máy chủ cấp và bạn xem được chính xác ở màn hình tài khoản sau khi
          đăng nhập.
        </p>
      )}
    </div>
  )
}

/** The permissions a role is notably *without*, keyed loosely because the role
 *  name arrives as a plain string from the API. */
function notableDenials(role: string): readonly string[] {
  return (NOTABLE_DENIALS as Record<string, readonly Capability[] | undefined>)[role] ?? []
}

function CapabilityRow({ code, granted }: { code: string; granted: boolean }) {
  const meta = CAPABILITIES.find((c) => c.id === code)
  return (
    <li className={`text-meta ${granted ? 'text-ink' : 'text-faint'}`}>
      <span aria-hidden>{granted ? '✓' : '✕'}</span>
      <span className="sr-only">{granted ? 'được cấp: ' : 'không được cấp: '}</span>{' '}
      <span className="font-mono">{code}</span>
      {meta && <span className="text-muted"> — {meta.label}</span>}
    </li>
  )
}

type DeadKind = 'missing' | 'expired' | 'used' | 'revoked' | 'unknown' | 'offline'

/** Reads the reason out of the problem document, when the server gives one. */
function kindOf(error: unknown): DeadKind {
  if (!(error instanceof RequestFailed)) return 'offline'
  switch (error.body.code) {
    case 'invitation_expired':
      return 'expired'
    case 'invitation_accepted':
    case 'invitation_used':
      return 'used'
    case 'invitation_revoked':
      return 'revoked'
    default:
      return 'unknown'
  }
}

const DEAD_TEXT: Record<DeadKind, { title: string; body: string }> = {
  missing: {
    title: 'Liên kết thiếu mã lời mời',
    body: 'Địa chỉ này không có phần token. Một số ứng dụng email cắt mất phần cuối của liên kết — hãy mở lại từ chính bức thư.',
  },
  expired: {
    title: 'Lời mời đã hết hạn',
    body: 'Lời mời sống 7 ngày. Nhờ người đã mời bạn gửi lại một lời mời mới — lời mời cũ không khôi phục được.',
  },
  used: {
    title: 'Lời mời này đã được dùng',
    body: 'Tài khoản đã được tạo bằng liên kết này. Hãy đăng nhập bằng email được mời; nếu quên mật khẩu, dùng chức năng đặt lại mật khẩu.',
  },
  revoked: {
    title: 'Lời mời đã bị thu hồi',
    body: 'Quản trị viên đã rút lại lời mời này. Liên hệ người đã mời bạn nếu đây là nhầm lẫn.',
  },
  unknown: {
    title: 'Lời mời này không còn hiệu lực',
    body: 'Liên kết có thể đã hết hạn, đã được dùng, hoặc đã bị thu hồi. Máy chủ cố ý không nói rõ là trường hợp nào — hãy nhờ người đã mời bạn gửi lại.',
  },
  offline: {
    title: 'Không kết nối được máy chủ',
    body: 'Lời mời chưa được kiểm tra. Kiểm tra mạng rồi tải lại trang.',
  },
}

function Dead({ kind, error }: { kind: DeadKind; error?: unknown }) {
  const text = DEAD_TEXT[kind]
  const traceId = error instanceof RequestFailed ? error.body.trace_id : undefined

  return (
    <Frame title="Lời mời tham gia">
      <div role="alert" className="rounded border border-overdue/40 bg-overdue/5 px-3 py-3">
        <p className="text-body font-semibold text-overdue">{text.title}</p>
        <p className="mt-1 text-meta leading-relaxed text-muted">{text.body}</p>
        {traceId && <p className="id-chip mt-1">mã tra cứu: {traceId}</p>}
      </div>
      <Link to="/login" className="btn mt-3 block text-center">
        Tới trang đăng nhập
      </Link>
    </Frame>
  )
}

function acceptErrorText(error: unknown): string {
  if (!(error instanceof RequestFailed)) {
    return 'Không kết nối được máy chủ. Kiểm tra mạng rồi thử lại.'
  }
  if (error.status === 404) {
    return 'Lời mời này vừa hết hiệu lực. Nhờ người đã mời bạn gửi lại.'
  }
  return error.body.title
}

function Frame({
  title,
  wide,
  children,
}: {
  title: string
  wide?: boolean
  children: React.ReactNode
}) {
  return (
    <div
      className={`mx-auto flex min-h-screen flex-col justify-center px-6 py-10 ${wide ? 'max-w-md' : 'max-w-sm'}`}
    >
      <div className="flex items-baseline justify-between gap-3">
        <h1 className="font-display text-2xl font-semibold">COLLECTR</h1>
        <StatusPill>lời mời</StatusPill>
      </div>
      <h2 className="mt-4 font-display text-[18px] font-semibold leading-tight">{title}</h2>
      <p className="id-chip mt-0.5">GET /api/auth/invitations/{'{token}'}</p>
      <div className="mt-4">{children}</div>
    </div>
  )
}
