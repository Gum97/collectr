import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { useNavigate } from 'react-router'
import { api, RequestFailed } from '../lib/api'

interface LoginResult {
  token?: string
  mfa_required?: boolean
  mfa_setup_required?: boolean
  tenant_id?: string
}

/** Sign-in, including the second step. Screens 2a in the wireframes.
 *
 * The two steps live in one component because they are one decision by the
 * person signing in; splitting them across routes would let a reload land on a
 * challenge screen with no password behind it. */
export function Login() {
  const [step, setStep] = useState<'password' | 'totp'>('password')
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [code, setCode] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const nav = useNavigate()
  const qc = useQueryClient()

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError('')
    try {
      // mfa_code, not code: the server rejects unknown fields, so the wrong name
      // does not get ignored -- it fails the whole request with a 400.
      const body =
        step === 'password' ? { email, password } : { email, password, mfa_code: code }
      const res = await api.post<LoginResult>('/api/auth/login', body)
      await qc.invalidateQueries({ queryKey: ['me'] })
      nav(res.mfa_setup_required ? '/account/mfa' : '/', { replace: true })
    } catch (err) {
      const failed = err instanceof RequestFailed ? err : null

      // "A second factor is needed" arrives as a 401, which is an error to the
      // fetch layer but not to the person: their password was correct. Reading
      // the code rather than the status is what separates the two, and getting
      // this wrong told people their credentials were wrong when they were not.
      if (failed?.body.code === 'mfa_required') {
        setStep('totp')
        setError('')
        return
      }

      // Deliberately one message for both a wrong password and an unknown
      // address. Distinguishing them turns this form into a way to test whether
      // somebody works here. The second factor is exempt: the password is
      // already proven by then, so naming the real problem reveals nothing.
      setError(
        failed?.status === 429
          ? 'Quá nhiều lần thử. Vui lòng đợi ít phút rồi thử lại.'
          : failed?.body.code === 'mfa_invalid'
            ? 'Mã không đúng hoặc đã hết hạn. Mã đổi mỗi 30 giây.'
            : step === 'totp'
              ? 'Không xác nhận được mã. Vui lòng thử lại.'
              : 'Email hoặc mật khẩu không đúng.',
      )
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="mx-auto flex min-h-screen max-w-sm flex-col justify-center px-6">
      <h1 className="font-display text-2xl font-semibold">COLLECTR</h1>
      <p className="mt-1 text-lede text-muted">
        {step === 'password' ? 'Đăng nhập để tiếp tục.' : 'Nhập mã từ ứng dụng xác thực.'}
      </p>

      <form onSubmit={submit} className="mt-6 flex flex-col gap-3">
        {step === 'password' ? (
          <>
            <Field label="Email">
              <input
                type="email"
                autoComplete="username"
                required
                autoFocus
                value={email}
                onChange={(e) => setEmail(e.target.value)}
                className="input"
              />
            </Field>
            <Field label="Mật khẩu">
              <input
                type="password"
                autoComplete="current-password"
                required
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                className="input"
              />
            </Field>
          </>
        ) : (
          <Field label="Mã 6 chữ số">
            <input
              // One-time-code so a phone offers the code from its keychain, and
              // numeric so the keyboard is the right one on mobile.
              inputMode="numeric"
              autoComplete="one-time-code"
              // The hyphen is escaped because the browser compiles `pattern`
              // with the RegExp `v` flag, where a bare `-` in a character class
              // is a syntax error. An invalid pattern does not fail loudly: the
              // attribute is discarded and the field then validates nothing at
              // all, which is why this read as working.
              pattern="[0-9]{6}|[a-z0-9\-]{8,}"
              required
              autoFocus
              value={code}
              onChange={(e) => setCode(e.target.value.trim())}
              className="input font-mono tracking-[0.3em]"
            />
            <p className="mt-1 text-meta text-muted">
              Mất thiết bị? Nhập một mã khôi phục — mỗi mã dùng được một lần.
            </p>
          </Field>
        )}

        {error && (
          <p role="alert" className="rounded border border-overdue/40 bg-overdue/5 px-3 py-2 text-body text-overdue">
            {error}
          </p>
        )}

        <button type="submit" disabled={busy} className="btn-primary mt-1">
          {busy ? 'Đang kiểm tra…' : step === 'password' ? 'Đăng nhập' : 'Xác nhận'}
        </button>

        {step === 'password' && (
          <a href="/password/forgot" className="text-center text-body text-accent hover:text-accent-dark">
            Quên mật khẩu?
          </a>
        )}
      </form>
    </div>
  )
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="flex flex-col gap-1">
      <span className="text-meta font-semibold">{label}</span>
      {children}
    </label>
  )
}
