/**
 * Declaring what the form collects data *for*.
 *
 * This lives in the builder rather than on the compliance screen because the
 * purposes are part of the schema, and the schema is versioned: a published
 * version is immutable and carries the purposes it was published with, so a
 * submission can always be traced to the exact declaration in force when it was
 * made. Editing them anywhere else would mean editing a version after the fact.
 *
 * Without this screen a form that collects personal data could be built and
 * never published: the validator refuses a schema with no declared purpose, and
 * nothing in the interface could add one.
 */
import { useQuery } from '@tanstack/react-query'
import { api, type List } from '../../lib/api'
import { Callout, Card, ErrorBanner, Loading } from '../../components/ui'
import type { DraftSchema } from './useDraft'

interface OrgPurpose {
  code: string
  name: string
  legal_basis: string
  description?: string
}

export function ConsentTab({
  schema,
  readOnly,
  onApply,
}: {
  schema: DraftSchema
  readOnly: boolean
  onApply: (next: DraftSchema) => void
}) {
  const purposes = useQuery({
    queryKey: ['consent-purposes'],
    queryFn: async () => (await api.get<List<OrgPurpose>>('/api/v1/consent/purposes')).data,
  })

  const declared = schema.consent?.purposes ?? []
  const has = (code: string) => declared.some((p) => p.code === code)

  const setConsent = (next: Partial<NonNullable<DraftSchema['consent']>>) =>
    onApply({ ...schema, consent: { ...schema.consent, ...next } })

  const toggle = (code: string, on: boolean) =>
    setConsent({
      purposes: on
        ? [...declared, { code, required: false }]
        : declared.filter((p) => p.code !== code),
    })

  const setRequired = (code: string, required: boolean) =>
    setConsent({ purposes: declared.map((p) => (p.code === code ? { ...p, required } : p)) })

  // Read off the schema, not off a checkbox the operator ticked. A field marked
  // sensitive is what creates the obligation; asking them to remember to
  // declare it separately is asking them to fail.
  const sensitiveFields = Object.entries(schema.fields ?? {})
    .filter(([, f]) => f.sensitive)
    .map(([, f]) => f.label)

  return (
    <div className="grid gap-4">
      <Card title="Mục đích xử lý" aside="đi kèm version, không sửa được sau khi publish">
        {purposes.isPending && <Loading />}
        {purposes.error && <ErrorBanner error={purposes.error} retry={() => void purposes.refetch()} />}

        {purposes.data?.length === 0 && (
          <Callout tone="duesoon" title="Tổ chức chưa khai báo mục đích nào">
            Tạo mục đích ở <span className="font-mono text-meta">Tuân thủ & DSR → Mục đích</span>{' '}
            trước, rồi quay lại đây chọn. Mục đích là tài sản của tổ chức, dùng lại cho nhiều biểu
            mẫu, nên nó không được tạo lẻ trong từng biểu mẫu.
          </Callout>
        )}

        <div className="mt-2 grid gap-2">
          {(purposes.data ?? []).map((p) => {
            const on = has(p.code)
            const ref = declared.find((d) => d.code === p.code)
            return (
              <div key={p.code} className="rounded border border-line p-2.5">
                <label className="flex items-start gap-2">
                  <input
                    type="checkbox"
                    checked={on}
                    disabled={readOnly}
                    onChange={(e) => toggle(p.code, e.target.checked)}
                  />
                  <span>
                    <span className="text-body font-medium">{p.name}</span>{' '}
                    <span className="font-mono text-meta text-muted">{p.code}</span>
                    <span className="block text-meta text-muted">
                      Căn cứ pháp lý: {p.legal_basis}
                    </span>
                  </span>
                </label>

                {on && (
                  <label className="mt-1.5 ml-6 flex items-center gap-2 text-meta">
                    <input
                      type="checkbox"
                      checked={Boolean(ref?.required)}
                      disabled={readOnly}
                      onChange={(e) => setRequired(p.code, e.target.checked)}
                    />
                    <span>
                      Bắt buộc — không tích ô này thì không gửi được biểu mẫu.{' '}
                      <span className="text-muted">
                        Chỉ đặt bắt buộc khi thiếu nó thì thật sự không phục vụ được; ép đồng ý
                        marketing để dùng dịch vụ thì đó không còn là đồng ý tự nguyện.
                      </span>
                    </span>
                  </label>
                )}
              </div>
            )
          })}
        </div>
      </Card>

      <Card title="Dữ liệu nhạy cảm">
        {sensitiveFields.length > 0 ? (
          <Callout tone="duesoon" title={`${sensitiveFields.length} câu hỏi được đánh dấu nhạy cảm`}>
            {sensitiveFields.join(' · ')}
            <br />
            Luật 91/2025 buộc phải nói rõ cho chủ thể biết là đang thu dữ liệu nhạy cảm — báo
            riêng, không lẫn vào văn bản đồng ý chung.
          </Callout>
        ) : (
          <p className="text-meta text-muted">
            Không có câu hỏi nào đánh dấu nhạy cảm. Nếu biểu mẫu thu CCCD, dữ liệu sức khoẻ hay
            sinh trắc học, hãy đánh dấu ở tab Soạn — đánh dấu là thứ khiến chúng được mã hoá riêng
            và bị xoá cùng khoá khi có yêu cầu xoá.
          </p>
        )}

        <label className="mt-2 flex items-start gap-2">
          <input
            type="checkbox"
            checked={Boolean(schema.consent?.sensitive_notice_required)}
            disabled={readOnly}
            onChange={(e) => setConsent({ sensitive_notice_required: e.target.checked })}
          />
          <span className="text-body">
            Hiện thông báo dữ liệu nhạy cảm trên trang điền form
            <span className="block text-meta text-muted">
              Bắt buộc bật khi có câu hỏi nhạy cảm, nếu không sẽ không publish được.
            </span>
          </span>
        </label>
      </Card>
    </div>
  )
}
