import { useQuery } from '@tanstack/react-query'
import { useParams } from 'react-router'
import { useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from 'react-router'
import { api, RequestFailed, type List } from '../lib/api'
import { num } from '../components/ui'

interface FormRow {
  id: string
  public_id: string
  title: string
  status: string
  live_version: number | null
  submission_count: number
  has_sensitive?: boolean
  updated_at: string
}

export function Forms() {
  const { projectId } = useParams()
  const forms = useQuery({
    queryKey: ['forms', projectId],
    queryFn: async () =>
      (await api.get<List<FormRow>>(`/api/v1/forms?project_id=${projectId}`)).data,
    enabled: Boolean(projectId),
  })

  return (
    <div className="p-6">
      <header className="mb-4 flex items-baseline justify-between">
        <div>
          <h1 className="text-[15px] font-semibold">Biểu mẫu</h1>
          <p className="id-chip">
            {forms.data
              ? `${forms.data.length} biểu mẫu · ${forms.data.filter((f) => f.status === 'draft').length} bản nháp`
              : ''}
          </p>
        </div>
        <CreateForm projectId={projectId!} />
      </header>

      {forms.isPending && <p className="text-body text-muted">Đang tải…</p>}
      {forms.isError && <p className="text-body text-overdue">Không tải được danh sách.</p>}

      {forms.data && (
        <table className="w-full border-collapse rounded border border-line bg-surface text-body">
          <thead>
            <tr className="border-b border-line text-left">
              <Th>Tên</Th>
              <Th>Version</Th>
              <Th className="text-right">Lượt gửi</Th>
              <Th>Trạng thái</Th>
            </tr>
          </thead>
          <tbody>
            {forms.data.map((f) => (
              <tr key={f.id} className="border-b border-line last:border-0">
                <td className="px-3 py-2">
                  <div className="font-semibold">{f.title}</div>
                  <div className="id-chip">
                    {f.public_id}
                    {/* Flagged in the list, not only inside the form: whether a
                        form collects sensitive data changes who may read its
                        responses and how they must be erased. */}
                    {f.has_sensitive && ' · có field nhạy cảm'}
                  </div>
                </td>
                <td className="px-3 py-2 font-mono text-meta">
                  {f.live_version ? `v${f.live_version} live` : 'chưa publish'}
                </td>
                <td className="px-3 py-2 text-right font-mono">{num(f.submission_count)}</td>
                <td className="px-3 py-2">
                  <StatusPill status={f.status} />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  )
}

function Th({ children, className = '' }: { children: React.ReactNode; className?: string }) {
  return <th className={`px-3 py-2 font-mono text-meta tracking-caps text-faint ${className}`}>{children}</th>
}

function StatusPill({ status }: { status: string }) {
  const label: Record<string, string> = {
    live: 'Đang chạy',
    draft: 'Nháp',
    closed: 'Đã đóng',
    archived: 'Lưu trữ',
  }
  return (
    <span className="rounded border border-line px-1.5 py-0.5 text-meta font-semibold">
      {label[status] ?? status}
    </span>
  )
}

/**
 * Creating a form, and then going straight into it.
 *
 * The title is asked for here rather than defaulted to "Biểu mẫu mới", because
 * it is the name a respondent sees at the top of the page they are filling in --
 * not an internal label somebody renames later.
 */
function CreateForm({ projectId }: { projectId: string }) {
  const [open, setOpen] = useState(false)
  const [title, setTitle] = useState('')
  const [error, setError] = useState('')
  const nav = useNavigate()
  const qc = useQueryClient()

  const create = useMutation({
    mutationFn: async () =>
      api.post<{ id: string }>('/api/v1/forms', { project_id: projectId, title: title.trim() }),
    onSuccess: async (form) => {
      await qc.invalidateQueries({ queryKey: ['forms', projectId] })
      // Into the builder, not back to the list: a form with no questions is not
      // a thing anybody wanted, it is a step on the way to one.
      nav(`/p/${projectId}/forms/${form.id}/builder`)
    },
    onError: (err) => {
      setError(
        err instanceof RequestFailed
          ? (err.fields.title ?? err.body.title)
          : 'Không tạo được biểu mẫu.',
      )
    },
  })

  if (!open) {
    return (
      <button className="btn-primary" onClick={() => setOpen(true)}>
        + Biểu mẫu mới
      </button>
    )
  }

  return (
    <form
      className="flex items-start gap-2"
      onSubmit={(e) => {
        e.preventDefault()
        setError('')
        if (title.trim()) create.mutate()
      }}
    >
      <div>
        <input
          autoFocus
          className="input w-64"
          placeholder="Tên biểu mẫu"
          value={title}
          onChange={(e) => setTitle(e.target.value)}
          onKeyDown={(e) => e.key === 'Escape' && setOpen(false)}
        />
        {error && (
          <p role="alert" className="mt-1 text-meta text-legal">
            {error}
          </p>
        )}
      </div>
      <button type="submit" className="btn-primary" disabled={!title.trim() || create.isPending}>
        {create.isPending ? 'Đang tạo…' : 'Tạo'}
      </button>
      <button type="button" className="btn" onClick={() => setOpen(false)}>
        Huỷ
      </button>
    </form>
  )
}
