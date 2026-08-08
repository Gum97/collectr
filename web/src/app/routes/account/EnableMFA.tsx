import { useEffect, useRef, useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Link, useNavigate } from 'react-router'
import { api, RequestFailed } from '../../lib/api'
import { Card, ErrorBanner, Loading, StatusPill } from '../../components/ui'

/** What `mfa/begin` hands back.
 *
 * `secret` and `uri` are what the server sends today; `qr_data_uri` is not sent
 * yet but is read if it ever appears, so a server-rendered code costs one field
 * rather than a QR encoder in the bundle. */
interface Enrolment {
  secret: string
  uri: string
  /** A rendered code, if the server ever sends one. Expected as a data: URI so
   *  it can go straight into an <img>. */
  qr_data_uri?: string
}

interface Confirmed {
  mfa_enabled: boolean
  recovery_codes: string[]
  message?: string
}

/**
 * Screen 2b: turning on the second factor, in three steps on one page.
 *
 * One page rather than three routes, because the middle of this flow is not a
 * place anyone should be able to land on or reload into: the secret is issued
 * once, shown once, and a reload would silently rotate it out from under an
 * authenticator app that had already scanned it.
 *
 * It also deliberately sits outside the signed-in shell. Confirming revokes
 * every session, including this one, so the last step runs with a dead cookie;
 * mounted inside a guard that redirects on a 401, the recovery codes would be
 * swept off the screen before they were written down.
 */
export function EnableMFA() {
  const [code, setCode] = useState('')
  const [codes, setCodes] = useState<string[] | null>(null)
  const nav = useNavigate()
  const qc = useQueryClient()

  const begin = useMutation({
    mutationFn: () => api.post<Enrolment>('/api/v1/auth/mfa/begin'),
  })
  const confirm = useMutation({
    mutationFn: (c: string) => api.post<Confirmed>('/api/v1/auth/mfa/confirm', { code: c }),
    onSuccess: (res) => setCodes(res.recovery_codes ?? []),
  })

  // Fired once, by hand, rather than through useQuery: this is a POST that mints
  // a new secret, and running it twice would invalidate a code the person has
  // already scanned. The ref survives the double effect invocation StrictMode
  // performs in development.
  const started = useRef(false)
  useEffect(() => {
    if (started.current) return
    started.current = true
    begin.mutate()
  }, [begin])

  const enrolment = begin.data
  const done = codes !== null

  function leave() {
    // Every session went with the confirmation, this one included. Drop the
    // cached identity so the login screen does not read a stale answer.
    qc.clear()
    nav('/login', { replace: true })
  }

  return (
    <div className="mx-auto min-h-screen max-w-3xl px-6 py-10">
      <h1 className="font-display text-2xl font-semibold">COLLECTR</h1>

      <div className="mb-4 mt-6 flex items-start justify-between gap-4">
        <div>
          <h2 className="text-[15px] font-semibold">Bảo mật tài khoản</h2>
          <p className="id-chip mt-0.5">/api/v1/auth/mfa/begin → /confirm</p>
        </div>
        {/* Stated as a requirement, not an invitation. Someone who reads this as
            an optional hardening step will postpone it, and these are the roles
            that can reach personal data across every project. */}
        <StatusPill tone="accent">Bắt buộc với vai trò Admin &amp; DPO</StatusPill>
      </div>

      <p className="mb-4 max-w-prose text-body text-muted">
        Vai trò của bạn yêu cầu xác thực 2 lớp — đây không phải tuỳ chọn. Cho tới khi hoàn tất ba
        bước dưới đây, bạn chưa dùng được các màn hình có dữ liệu cá nhân.
      </p>

      <Card>
        <div className="flex flex-col gap-5 md:flex-row">
          <div className="md:w-56 md:shrink-0">
            <StepTitle n={1} done={Boolean(enrolment)}>
              Quét bằng ứng dụng xác thực
            </StepTitle>

            {begin.isPending && <Loading label="Đang tạo khoá…" />}
            {begin.isError && <ErrorBanner error={begin.error} retry={() => begin.mutate()} />}
            {enrolment && <Enrol enrolment={enrolment} />}
          </div>

          <div className="flex min-w-0 flex-1 flex-col gap-5">
            <div>
              <StepTitle n={2} done={done}>
                Nhập mã hiện trên ứng dụng
              </StepTitle>

              <form
                onSubmit={(e) => {
                  e.preventDefault()
                  confirm.mutate(code)
                }}
                className="flex flex-wrap items-start gap-2"
              >
                <label className="sr-only" htmlFor="mfa-code">
                  Mã 6 chữ số
                </label>
                <input
                  id="mfa-code"
                  inputMode="numeric"
                  autoComplete="one-time-code"
                  pattern="[0-9]{6}"
                  maxLength={6}
                  required
                  disabled={done || !enrolment}
                  value={code}
                  onChange={(e) => setCode(e.target.value.replace(/\D/g, ''))}
                  className="input w-32 font-mono tracking-[0.3em]"
                />
                <button type="submit" disabled={done || confirm.isPending} className="btn-primary">
                  {confirm.isPending ? 'Đang kiểm tra…' : 'Xác nhận'}
                </button>
              </form>

              {confirm.isError && (
                <p role="alert" className="mt-2 text-meta font-semibold text-overdue">
                  {confirm.error instanceof RequestFailed
                    ? confirm.error.body.title
                    : 'Không gửi được mã. Kiểm tra kết nối rồi thử lại.'}
                </p>
              )}

              <p className="mt-1.5 text-meta text-muted">
                Chưa bật cho tới khi mã đầu tiên khớp — tránh khoá chính mình ra ngoài.
              </p>
            </div>

            <div>
              <StepTitle n={3} done={done}>
                {codes ? `${codes.length} mã khôi phục — chỉ hiện lần này` : 'Mã khôi phục'}
              </StepTitle>

              {codes ? (
                <RecoveryCodesPanel
                  codes={codes}
                  note="Xác nhận xong, mọi phiên đăng nhập cũ đã bị thu hồi — kể cả phiên này. Bạn sẽ cần đăng nhập lại."
                  actionLabel="Tôi đã lưu — đăng nhập lại"
                  onAcknowledge={leave}
                />
              ) : (
                <p className="rounded border border-dashed border-line px-3 py-3 text-meta text-muted">
                  Mã khôi phục xuất hiện ở đây sau khi mã đầu tiên khớp. Chúng chỉ hiện đúng một lần
                  — chuẩn bị sẵn chỗ lưu trước khi bấm Xác nhận.
                </p>
              )}
            </div>
          </div>
        </div>
      </Card>

      {!done && (
        <p className="mt-4 text-center text-meta text-muted">
          Chưa sẵn sàng?{' '}
          <Link to="/account" className="text-accent hover:text-accent-dark">
            Quay lại trang tài khoản
          </Link>{' '}
          — xác thực 2 lớp vẫn chưa bật cho tới khi bạn hoàn tất ba bước trên.
        </p>
      )}
    </div>
  )
}

function StepTitle({ n, done, children }: { n: number; done: boolean; children: React.ReactNode }) {
  return (
    <h3 className="mb-2 text-meta font-semibold">
      {/* A tick as well as a colour: two of the three steps differ only by state,
          and colour alone does not carry that for every reader. */}
      <span className={done ? 'text-ok' : 'text-muted'} aria-hidden>
        {done ? '✓' : `${n} ·`}
      </span>{' '}
      {children}
    </h3>
  )
}

/** The secret side of enrolment.
 *
 * There is no QR image: `mfa/begin` returns the secret and the `otpauth://` URI
 * and nothing else, and drawing a QR in the browser would mean adding an
 * encoder. The URI is offered as a link instead -- on a phone it opens the
 * authenticator directly -- with the secret formatted for typing in by hand on
 * a desktop. If the server starts returning a rendered code, it is shown here.
 */
function Enrol({ enrolment }: { enrolment: Enrolment }) {
  const grouped = enrolment.secret.replace(/(.{4})/g, '$1 ').trim()

  return (
    <div>
      {enrolment.qr_data_uri ? (
        // Rendered as an image source, never injected as markup: an SVG dropped
        // into the DOM carries scripts with it, and this one would be injected
        // into a page that is holding a TOTP secret.
        <img
          src={enrolment.qr_data_uri}
          alt="Mã QR chứa khoá TOTP của tài khoản này"
          className="mb-2 size-[150px] rounded border border-line"
        />
      ) : (
        <div className="mb-2 flex flex-col gap-2 rounded border border-dashed border-line p-3">
          <p className="text-meta text-muted">
            Máy chủ chưa gửi ảnh QR. Dùng một trong hai cách dưới đây.
          </p>
          <a href={enrolment.uri} className="btn text-center">
            Mở ứng dụng xác thực
          </a>
        </div>
      )}

      <p className="text-meta font-semibold">Hoặc nhập khoá thủ công</p>
      <p className="mt-1 select-all break-all font-mono text-body tracking-wider">{grouped}</p>
      <CopyButton value={enrolment.secret} label="Sao chép khoá" />

      <p className="mt-2 break-all font-mono text-meta leading-relaxed text-faint">
        {enrolment.uri}
      </p>
      <p className="mt-2 text-meta text-muted">
        Khoá này chỉ hiện một lần. Không lưu nó vào trình duyệt hay trình quản lý mật khẩu dùng
        chung với mật khẩu tài khoản.
      </p>
    </div>
  )
}

/**
 * The one-time list of recovery codes.
 *
 * Shared with the account screen, which shows the same thing after a
 * regeneration. Both places carry the same obligation: the codes exist only in
 * this response, nothing here writes them anywhere, and the person has to say
 * they have stored them before the list can be dismissed.
 */
export function RecoveryCodesPanel({
  codes,
  note,
  actionLabel,
  onAcknowledge,
}: {
  codes: string[]
  note?: string
  actionLabel: string
  onAcknowledge: () => void
}) {
  const [saved, setSaved] = useState(false)

  // Closing the tab here loses the codes for good. The browser's own warning is
  // the only thing that can interrupt that.
  useEffect(() => {
    const warn = (e: BeforeUnloadEvent) => e.preventDefault()
    window.addEventListener('beforeunload', warn)
    return () => window.removeEventListener('beforeunload', warn)
  }, [])

  return (
    <div className="rounded border border-accent bg-accent/5 p-3" role="status">
      <p className="mb-2 text-meta font-semibold text-accent">
        Lưu ngay — danh sách này không hiện lại lần nào nữa
      </p>

      <ul className="grid grid-cols-2 gap-x-4 gap-y-1 font-mono text-body">
        {codes.map((c) => (
          <li key={c} className="select-all">
            {c}
          </li>
        ))}
      </ul>

      <p className="mt-2 text-meta text-muted">
        Mỗi mã dùng được một lần, thay cho mã trên ứng dụng khi bạn mất thiết bị.
      </p>
      {note && <p className="mt-1 text-meta text-muted">{note}</p>}

      <div className="mt-3 flex flex-wrap items-center gap-2">
        <button type="button" className="btn" onClick={() => downloadCodes(codes)}>
          Tải .txt
        </button>
        <CopyButton value={codes.join('\n')} label="Sao chép" />
      </div>

      <label className="mt-3 flex items-center gap-2 text-meta">
        <input
          type="checkbox"
          checked={saved}
          onChange={(e) => setSaved(e.target.checked)}
          className="size-3.5 accent-accent"
        />
        Tôi đã lưu các mã này ở nơi an toàn
      </label>

      <button type="button" disabled={!saved} onClick={onAcknowledge} className="btn-primary mt-2">
        {actionLabel}
      </button>
    </div>
  )
}

function CopyButton({ value, label }: { value: string; label: string }) {
  const [state, setState] = useState<'idle' | 'done' | 'failed'>('idle')

  async function copy() {
    try {
      await navigator.clipboard.writeText(value)
      setState('done')
    } catch {
      // Clipboard access is refused outside a secure context. Say so rather than
      // showing "đã sao chép" over an empty clipboard.
      setState('failed')
    }
  }

  return (
    <span className="inline-flex items-center gap-2">
      <button type="button" className="btn mt-2" onClick={copy}>
        {label}
      </button>
      {state !== 'idle' && (
        <span role="status" className={`mt-2 text-meta ${state === 'done' ? 'text-ok' : 'text-overdue'}`}>
          {state === 'done' ? 'đã sao chép' : 'trình duyệt chặn sao chép — hãy chọn và copy tay'}
        </span>
      )}
    </span>
  )
}

/** Writes the codes to a file without ever putting them in storage. */
function downloadCodes(codes: string[]) {
  const body = [
    'Mã khôi phục Collectr',
    `Tạo ngày ${new Date().toLocaleString('vi-VN')}`,
    'Mỗi mã dùng được một lần. Giữ tệp này ở nơi an toàn.',
    '',
    ...codes,
    '',
  ].join('\n')

  const url = URL.createObjectURL(new Blob([body], { type: 'text/plain;charset=utf-8' }))
  const a = document.createElement('a')
  a.href = url
  a.download = 'collectr-recovery-codes.txt'
  a.click()
  URL.revokeObjectURL(url)
}
