import { useState } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { Link, useNavigate, useSearchParams } from 'react-router'
import { api, RequestFailed } from '../../lib/api'
import { Loading } from '../../components/ui'
import { checkPassword, type PasswordCheck } from './password'

interface ResetPreview {
  email: string
  mfa_required: boolean
}

/**
 * Screen 3a, step two: the page the link in the email opens.
 *
 * The token is validated before the form is drawn, so somebody who waited a day
 * finds out the link expired instead of typing a password into a form that was
 * never going to work.
 */
export function ResetPassword() {
  const [params] = useSearchParams()
  const token = params.get('token') ?? ''
  const nav = useNavigate()

  const [password, setPassword] = useState('')
  const [reveal, setReveal] = useState(false)
  const [mfaCode, setMfaCode] = useState('')
  const [needsCode, setNeedsCode] = useState(false)
  const [done, setDone] = useState(false)

  const preview = useQuery({
    queryKey: ['reset-preview', token],
    queryFn: async () => {
      const res = await api.get<ResetPreview>(
        `/api/auth/password/reset/${encodeURIComponent(token)}`,
      )
      // The account carries a second factor, so the form has to ask for it. A
      // reset that stepped around MFA would hand the account to whoever holds
      // the mailbox, which is the one thing the second factor exists to stop.
      setNeedsCode(res.mfa_required)
      return res
    },
    enabled: token !== '',
    retry: false,
    staleTime: Infinity,
  })

  const submit = useMutation({
    mutationFn: () =>
      api.post('/api/auth/password/reset', {
        token,
        password,
        ...(needsCode ? { mfa_code: mfaCode.trim() } : {}),
      }),
    onSuccess: () => setDone(true),
    onError: (err) => {
      // A 401 on an account the preview said had no second factor means the
      // preview is stale, not that the password was wrong. Reveal the field
      // rather than reporting a failure the person cannot act on.
      if (err instanceof RequestFailed && err.status === 401) setNeedsCode(true)
    },
  })

  const check = checkPassword(password, { email: preview.data?.email })
  const fieldErrors = submit.error instanceof RequestFailed ? submit.error.fields : {}

  if (token === '') return <Broken />
  if (preview.isPending) return <Frame title="Đặt mật khẩu mới"><Loading label="Đang kiểm tra liên kết…" /></Frame>
  if (preview.isError) return <Expired error={preview.error} />

  if (done) {
    return (
      <Frame title="Đã đặt lại mật khẩu">
        <div role="status" className="rounded border border-ok/40 bg-ok/5 px-3 py-3">
          <p className="text-body font-semibold text-ok">Mật khẩu mới đã có hiệu lực</p>
          <p className="mt-1 text-meta leading-relaxed text-muted">
            Mọi phiên đang mở đã bị thu hồi — kể cả trên các thiết bị khác — và liên kết vừa dùng
            đã hết hiệu lực. Xác thực 2 lớp giữ nguyên như trước.
          </p>
        </div>
        <button
          type="button"
          className="btn-primary mt-3 w-full"
          onClick={() => nav('/login', { replace: true })}
        >
          Đăng nhập
        </button>
      </Frame>
    )
  }

  return (
    <Frame title="Đặt mật khẩu mới">
      <p className="text-meta text-muted">
        Đặt lại cho <span className="font-mono text-meta text-ink">{preview.data.email}</span>
      </p>

      <form
        onSubmit={(e) => {
          e.preventDefault()
          submit.mutate()
        }}
        className="mt-4 flex flex-col gap-3"
      >
        <div className="flex flex-col gap-1">
          <div className="flex items-baseline justify-between">
            <label htmlFor="new-password" className="text-meta font-semibold">
              Mật khẩu mới
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
            id="new-password"
            type={reveal ? 'text' : 'password'}
            autoComplete="new-password"
            required
            autoFocus
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

        {needsCode && (
          <div className="flex flex-col gap-1">
            <label htmlFor="reset-mfa" className="text-meta font-semibold">
              Mã xác thực 2 lớp
            </label>
            <input
              id="reset-mfa"
              inputMode="numeric"
              autoComplete="one-time-code"
              required
              value={mfaCode}
              onChange={(e) => setMfaCode(e.target.value)}
              className="input font-mono tracking-[0.2em]"
            />
            <span className="text-meta text-muted">
              Mã 6 số từ ứng dụng xác thực, hoặc một mã khôi phục nếu bạn mất thiết bị.
            </span>
          </div>
        )}

        {submit.isError && (
          <p
            role="alert"
            className="rounded border border-overdue/40 bg-overdue/5 px-3 py-2 text-body text-overdue"
          >
            {resetErrorText(submit.error, needsCode)}
          </p>
        )}

        <button type="submit" disabled={!check.ok || submit.isPending} className="btn-primary mt-1">
          {submit.isPending ? 'Đang đặt lại…' : 'Đặt lại & đăng nhập'}
        </button>
      </form>

      <p className="mt-3 text-meta leading-relaxed text-muted">
        Đặt lại xong: mọi phiên đang mở bị thu hồi, token này hết hiệu lực, và xác thực 2 lớp vẫn
        giữ nguyên — mật khẩu mới không đi vòng qua nó.
      </p>
    </Frame>
  )
}

function resetErrorText(error: unknown, hadCodeField: boolean): string {
  if (!(error instanceof RequestFailed)) {
    return 'Không kết nối được máy chủ. Kiểm tra mạng rồi thử lại.'
  }
  if (error.status === 401) {
    return hadCodeField
      ? 'Mã xác thực không đúng hoặc đã hết hạn. Thử mã mới nhất trên ứng dụng.'
      : 'Tài khoản này có xác thực 2 lớp. Nhập thêm mã ở ô vừa hiện ra.'
  }
  if (error.status === 404) {
    return 'Liên kết này không còn hiệu lực. Hãy yêu cầu một liên kết mới.'
  }
  // Field-level messages are already printed beside their input; this is the
  // catch-all for everything that has no field to sit next to.
  return error.body.title
}

/**
 * The strength bar and the rule list from the wireframe.
 *
 * Three states per rule, not two. A rule the browser could not judge shows a
 * neutral mark and says why -- a tick beside "không nằm trong danh sách mật khẩu
 * bị lộ" would claim a check that nothing here performed.
 */
export function PasswordMeter({ check }: { check: PasswordCheck }) {
  return (
    <div>
      <div className="flex gap-1" aria-hidden>
        {[0, 1, 2, 3].map((i) => (
          <span
            key={i}
            className={`h-1 flex-1 rounded ${i < check.score ? 'bg-ink' : 'bg-chrome'}`}
          />
        ))}
      </div>
      <p role="status" className="mt-1 text-meta text-muted">
        Độ mạnh: {check.scoreLabel}
      </p>

      <ul className="mt-2 flex flex-col gap-1">
        {check.rules.map((r) => (
          <li
            key={r.id}
            className={`text-meta ${
              r.state === 'fail' ? 'text-overdue' : r.state === 'pass' ? 'text-ink' : 'text-muted'
            }`}
          >
            <span aria-hidden>{r.state === 'pass' ? '✓' : r.state === 'fail' ? '✕' : '?'}</span>{' '}
            <span className="sr-only">
              {r.state === 'pass' ? 'đạt: ' : r.state === 'fail' ? 'chưa đạt: ' : 'chưa kiểm tra: '}
            </span>
            {r.label}
            {r.detail && <span className="text-muted"> — {r.detail}</span>}
          </li>
        ))}
      </ul>
    </div>
  )
}

function Frame({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div className="mx-auto flex min-h-screen max-w-sm flex-col justify-center px-6 py-10">
      <h1 className="font-display text-2xl font-semibold">COLLECTR</h1>
      <p className="mt-1 text-lede text-muted">{title}</p>
      <div className="mt-6">{children}</div>
    </div>
  )
}

function Broken() {
  return (
    <Frame title="Đặt mật khẩu mới">
      <div role="alert" className="rounded border border-overdue/40 bg-overdue/5 px-3 py-3">
        <p className="text-body font-semibold text-overdue">Liên kết thiếu mã xác nhận</p>
        <p className="mt-1 text-meta leading-relaxed text-muted">
          Địa chỉ này không có phần <span className="font-mono">token</span>. Một số ứng dụng email
          cắt mất phần cuối của liên kết — hãy mở lại từ chính bức thư, hoặc yêu cầu liên kết mới.
        </p>
      </div>
      <Link to="/password/forgot" className="btn mt-3 block text-center">
        Yêu cầu liên kết mới
      </Link>
    </Frame>
  )
}

function Expired({ error }: { error: unknown }) {
  const title =
    error instanceof RequestFailed && error.status === 404
      ? error.body.title
      : 'Không kiểm tra được liên kết. Thử lại sau ít phút.'

  return (
    <Frame title="Đặt mật khẩu mới">
      <div role="alert" className="rounded border border-overdue/40 bg-overdue/5 px-3 py-3">
        <p className="text-body font-semibold text-overdue">{title}</p>
        <p className="mt-1 text-meta leading-relaxed text-muted">
          Liên kết đặt lại sống 60 phút và chỉ dùng được một lần — nếu bạn đã đổi mật khẩu bằng
          liên kết này thì nó đã hết hiệu lực. Mật khẩu hiện tại của bạn không thay đổi.
        </p>
      </div>
      <Link to="/password/forgot" className="btn mt-3 block text-center">
        Yêu cầu liên kết mới
      </Link>
    </Frame>
  )
}
