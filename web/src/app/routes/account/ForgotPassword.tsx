import { useState } from 'react'
import { Link } from 'react-router'
import { api, RequestFailed } from '../../lib/api'

/**
 * Screen 3a, step one: ask for the address, say nothing about it.
 *
 * The single most important property of this page is that it behaves
 * identically for an address that has an account and one that does not. The
 * server already guarantees that -- it answers 202 to everything, sends the mail
 * from a detached goroutine so the timing does not leak either, and returns the
 * same body when it is rate limiting. This page must not undo that by rendering
 * anything derived from the response.
 *
 * So the confirmation text below is a constant in this file, not the server's
 * message, and an HTTP error is shown as the same confirmation: a 500 tells an
 * attacker nothing about the address, but a visibly different screen for some
 * addresses and not others would.
 */
export function ForgotPassword() {
  const [email, setEmail] = useState('')
  const [sent, setSent] = useState(false)
  const [offline, setOffline] = useState(false)
  const [busy, setBusy] = useState(false)

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    setBusy(true)
    setOffline(false)
    try {
      await api.post('/api/auth/password/forgot', { email })
      setSent(true)
    } catch (err) {
      if (err instanceof RequestFailed) {
        // The server answered. Whatever it answered, the person on this page
        // learns the same sentence as everybody else.
        setSent(true)
      } else {
        // The request never left the browser, which cannot depend on whether the
        // address exists -- so saying so discloses nothing and stops someone
        // waiting for mail that was never asked for.
        setOffline(true)
      }
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="mx-auto flex min-h-screen max-w-sm flex-col justify-center px-6">
      <h1 className="font-display text-2xl font-semibold">COLLECTR</h1>
      <p className="mt-1 text-lede text-muted">Quên mật khẩu</p>

      {sent ? (
        <div className="mt-6">
          <div
            role="status"
            className="rounded border border-dashed border-line bg-panel px-3 py-3"
          >
            <p className="text-body font-semibold">Đã gửi nếu email này có tài khoản</p>
            <p className="mt-1 text-meta leading-relaxed text-muted">
              Màn hình này trả về cùng một thông báo dù email có tồn tại hay không — người lạ không
              dò được ai đang dùng hệ thống. Liên kết sống 60 phút và chỉ dùng được một lần.
            </p>
            <p className="mt-2 text-meta leading-relaxed text-muted">
              Không thấy thư sau vài phút? Kiểm tra hộp thư rác, rồi thử gửi lại.
            </p>
          </div>

          <button
            type="button"
            className="btn mt-3 w-full"
            onClick={() => {
              setSent(false)
              setEmail('')
            }}
          >
            Gửi lại
          </button>
          <Link
            to="/login"
            className="mt-3 block text-center text-body text-accent hover:text-accent-dark"
          >
            Quay lại đăng nhập
          </Link>
        </div>
      ) : (
        <form onSubmit={submit} className="mt-6 flex flex-col gap-3">
          <label className="flex flex-col gap-1">
            <span className="text-meta font-semibold">Email</span>
            <input
              type="email"
              autoComplete="username"
              required
              autoFocus
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              className="input"
            />
          </label>

          {offline && (
            <p
              role="alert"
              className="rounded border border-overdue/40 bg-overdue/5 px-3 py-2 text-body text-overdue"
            >
              Không kết nối được máy chủ, chưa có yêu cầu nào được gửi đi. Kiểm tra mạng rồi thử
              lại.
            </p>
          )}

          <button type="submit" disabled={busy} className="btn-primary mt-1">
            {busy ? 'Đang gửi…' : 'Gửi liên kết đặt lại'}
          </button>

          <p className="text-meta leading-relaxed text-muted">
            Chúng tôi không cho biết địa chỉ này có tài khoản hay không. Nếu có, một liên kết đặt
            lại sẽ tới hộp thư trong ít phút.
          </p>

          <Link
            to="/login"
            className="text-center text-body text-accent hover:text-accent-dark"
          >
            Quay lại đăng nhập
          </Link>
        </form>
      )}
    </div>
  )
}
