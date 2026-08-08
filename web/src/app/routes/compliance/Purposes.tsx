/**
 * Processing purposes -- the "Mục đích" tab of the compliance centre.
 *
 * One purpose is one answer to "why are we allowed to hold this, and for how
 * long". Consent is only one of the lawful bases the law recognises, so the
 * basis is recorded per purpose rather than assumed: a purpose held under a
 * contract does not disappear when someone withdraws consent, and a purpose held
 * under consent must stop the moment they do.
 */
import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useSearchParams } from 'react-router'
import { RequestFailed, api, type List } from '../../lib/api'
import { retentionLabel } from '../../lib/projects'
import { can, useMe } from '../../lib/session'
import {
  Card,
  Empty,
  ErrorBanner,
  Field,
  Loading,
  StatusPill,
  Table,
  Td,
  Th,
  Tr,
} from '../../components/ui'

export type LegalBasis = 'consent' | 'contract' | 'legal_obligation' | 'vital_interest'

export interface Purpose {
  id: string
  code: string
  name: string
  description?: string
  legal_basis: LegalBasis | string
  /** The API writes `required` and the table column is `is_required`; accept
   *  either rather than silently reading a missing field as "tùy chọn". */
  required?: boolean
  is_required?: boolean
  retention_days?: number | null
}

export function usePurposes() {
  return useQuery({
    queryKey: ['consent-purposes'],
    queryFn: async () => (await api.get<List<Purpose>>('/api/v1/consent/purposes')).data,
    staleTime: 60_000,
  })
}

export function purposeRequired(p: Purpose): boolean {
  return Boolean(p.required ?? p.is_required)
}

export function legalBasisLabel(basis: string): string {
  switch (basis) {
    case 'consent':
      return 'Đồng ý'
    case 'contract':
      return 'Hợp đồng'
    case 'legal_obligation':
      return 'Nghĩa vụ pháp lý'
    case 'vital_interest':
      return 'Lợi ích thiết yếu'
    default:
      return basis
  }
}

/** One line summarising a purpose the way the form screens show it:
 *  `marketing · tùy chọn · lưu 12 tháng`. */
export function purposeSummary(p: Purpose): string {
  return [
    p.code,
    purposeRequired(p) ? 'bắt buộc' : 'tùy chọn',
    retentionLabel(p.retention_days ?? null),
  ].join(' · ')
}

export function Purposes() {
  const me = useMe()
  const purposes = usePurposes()
  const mayManage = can(me.data, 'consent.manage')

  const [params, setParams] = useSearchParams()
  const composing = params.get('new') === '1'

  function setComposing(on: boolean) {
    const next = new URLSearchParams(params)
    if (on) next.set('new', '1')
    else next.delete('new')
    setParams(next, { replace: true })
  }

  const rows = purposes.data ?? []

  return (
    <div className="flex flex-col gap-3">
      {mayManage && !composing && (
        <div className="flex justify-end">
          <button type="button" className="btn-primary" onClick={() => setComposing(true)}>
            + Mục đích
          </button>
        </div>
      )}
      {!mayManage && (
        <p className="id-chip text-right">
          Chỉ vai trò có <span className="font-semibold">consent.manage</span> thêm được mục đích.
        </p>
      )}

      {composing && mayManage && <Composer onDone={() => setComposing(false)} />}

      {purposes.isPending && <Loading />}
      {purposes.isError && (
        <ErrorBanner error={purposes.error} retry={() => void purposes.refetch()} />
      )}

      {purposes.data && rows.length === 0 && (
        <Empty
          title="Chưa khai báo mục đích xử lý nào"
          hint="Mỗi biểu mẫu phải nói rõ dữ liệu được dùng để làm gì và giữ trong bao lâu. Chưa có mục đích nào thì chưa có căn cứ pháp lý để gắn vào biểu mẫu."
        />
      )}

      {rows.length > 0 && (
        <Table
          head={
            <>
              <Th>Mục đích</Th>
              <Th>Căn cứ pháp lý</Th>
              <Th>Bắt buộc</Th>
              <Th>Hạn lưu</Th>
            </>
          }
        >
          {rows.map((p) => (
            <Tr key={p.id}>
              <Td>
                <div className="font-semibold">{p.name}</div>
                <div className="id-chip">{p.code}</div>
                {p.description && <p className="mt-0.5 text-meta text-muted">{p.description}</p>}
              </Td>
              <Td>
                <StatusPill tone={p.legal_basis === 'consent' ? 'accent' : 'neutral'}>
                  {legalBasisLabel(p.legal_basis)}
                </StatusPill>
              </Td>
              <Td className="text-meta">{purposeRequired(p) ? 'bắt buộc' : 'tùy chọn'}</Td>
              <Td className="whitespace-nowrap text-meta">
                {retentionLabel(p.retention_days ?? null)}
              </Td>
            </Tr>
          ))}
        </Table>
      )}

      <p className="id-chip">
        Rút đồng ý chỉ dừng xử lý cho mục đích dùng căn cứ “Đồng ý”. Mục đích dựa trên hợp đồng hay
        nghĩa vụ pháp lý vẫn tiếp tục — và phải nói được vì sao.
      </p>
    </div>
  )
}

function Composer({ onDone }: { onDone: () => void }) {
  const qc = useQueryClient()
  const [code, setCode] = useState('')
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [legalBasis, setLegalBasis] = useState<LegalBasis>('consent')
  const [required, setRequired] = useState(false)
  const [retentionDays, setRetentionDays] = useState('')

  const create = useMutation({
    mutationFn: (body: {
      code: string
      name: string
      description: string
      legal_basis: LegalBasis
      required: boolean
      retention_days: number | null
    }) => api.post<{ id: string; code: string }>('/api/v1/consent/purposes', body),
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: ['consent-purposes'] })
      onDone()
    },
  })

  const fields = create.error instanceof RequestFailed ? create.error.fields : {}

  return (
    <Card title="Thêm mục đích xử lý">
      <form
        className="flex flex-col gap-3"
        onSubmit={(e) => {
          e.preventDefault()
          create.mutate({
            code: code.trim(),
            name: name.trim(),
            description: description.trim(),
            legal_basis: legalBasis,
            required,
            // Empty means "no limit set here"; it must not become 0 days.
            retention_days: retentionDays.trim() === '' ? null : Number(retentionDays),
          })
        }}
      >
        <div className="grid gap-3 sm:grid-cols-2">
          <Field label="Mã" hint="Dùng trong schema biểu mẫu, ví dụ service" error={fields.code}>
            <input
              id="purpose-code"
              className="input font-mono"
              value={code}
              onChange={(e) => setCode(e.target.value)}
              required
            />
          </Field>
          <Field label="Tên hiển thị" error={fields.name}>
            <input
              id="purpose-name"
              className="input"
              value={name}
              onChange={(e) => setName(e.target.value)}
              required
            />
          </Field>
        </div>

        <Field label="Mô tả" hint="Chủ thể dữ liệu đọc dòng này khi quyết định đồng ý.">
          <input
            id="purpose-desc"
            className="input"
            value={description}
            onChange={(e) => setDescription(e.target.value)}
          />
        </Field>

        <div className="grid gap-3 sm:grid-cols-2">
          <Field label="Căn cứ pháp lý" error={fields.legal_basis}>
            <select
              id="purpose-basis"
              className="input"
              value={legalBasis}
              onChange={(e) => setLegalBasis(e.target.value as LegalBasis)}
            >
              <option value="consent">Đồng ý</option>
              <option value="contract">Hợp đồng</option>
              <option value="legal_obligation">Nghĩa vụ pháp lý</option>
              <option value="vital_interest">Lợi ích thiết yếu</option>
            </select>
          </Field>
          <Field
            label="Hạn lưu (ngày)"
            hint="Bỏ trống để dùng hạn lưu của biểu mẫu."
            error={fields.retention_days}
          >
            <input
              id="purpose-retention"
              type="number"
              min={1}
              className="input"
              value={retentionDays}
              onChange={(e) => setRetentionDays(e.target.value)}
            />
          </Field>
        </div>

        <label className="flex items-center gap-2 text-body">
          <input
            type="checkbox"
            checked={required}
            onChange={(e) => setRequired(e.target.checked)}
          />
          <span>
            Bắt buộc — không đồng ý thì không gửi được biểu mẫu.{' '}
            <span className="text-muted">
              Chỉ đặt cho mục đích thật sự cần để cung cấp dịch vụ.
            </span>
          </span>
        </label>

        {create.isError && !Object.keys(fields).length && <ErrorBanner error={create.error} />}

        <div className="flex gap-2">
          <button type="submit" className="btn-primary" disabled={create.isPending}>
            {create.isPending ? 'Đang lưu…' : 'Lưu'}
          </button>
          <button type="button" className="btn" onClick={onDone}>
            Huỷ
          </button>
        </div>
      </form>
    </Card>
  )
}
