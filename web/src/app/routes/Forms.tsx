import { useQuery } from '@tanstack/react-query'
import { useParams } from 'react-router'
import { api, type List } from '../lib/api'
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
        <button className="btn-primary">+ Biểu mẫu mới</button>
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
