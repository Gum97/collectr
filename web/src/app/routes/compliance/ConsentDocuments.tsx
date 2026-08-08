/**
 * Consent documents -- the "Văn bản đồng ý" tab of the compliance centre.
 *
 * A published version is immutable in the database (a trigger refuses UPDATE and
 * DELETE), because the only thing a consent record has to prove is what the
 * person was actually shown. Editing is therefore not offered anywhere here:
 * the single write is "publish the next version".
 */
import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useSearchParams } from 'react-router'
import { RequestFailed, api, type List } from '../../lib/api'
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
  date,
} from '../../components/ui'

export type DocumentKind = 'consent_text' | 'privacy_notice'

export interface ConsentDocument {
  id: string
  kind: DocumentKind | string
  version_no: number
  /** Already prefixed by the API: "sha256:9a1f…". */
  content_hash: string
  /** The API returns one of these; both are optional until the list endpoint
   *  exists, and an absent date is shown as such rather than as today. */
  effective_from?: string | null
  created_at?: string | null
  created_by_name?: string | null
  permalink?: string | null
}

interface CreatedDocument {
  id: string
  version_no: number
  content_hash: string
  permalink: string
}

export function useConsentDocuments() {
  return useQuery({
    queryKey: ['consent-documents'],
    queryFn: async () => (await api.get<List<ConsentDocument>>('/api/v1/consent/documents')).data,
    staleTime: 60_000,
  })
}

export function documentKindLabel(kind: string): string {
  switch (kind) {
    case 'consent_text':
      return 'Văn bản đồng ý'
    case 'privacy_notice':
      return 'Thông báo xử lý DLCN'
    default:
      return kind
  }
}

/**
 * activeDocument is the version a form collecting consent right now would use.
 *
 * The server resolves it per tenant and kind, not per form, so the highest
 * version number of that kind is the one being shown to respondents.
 */
export function activeDocument(
  docs: ConsentDocument[] | undefined,
  kind: DocumentKind = 'consent_text',
): ConsentDocument | null {
  if (!docs) return null
  const ofKind = docs.filter((d) => d.kind === kind)
  if (ofKind.length === 0) return null
  return ofKind.reduce((newest, d) => (d.version_no > newest.version_no ? d : newest))
}

/** publishedAt reads whichever timestamp the API supplied for a version. */
export function publishedAt(doc: ConsentDocument): string | null {
  return doc.effective_from ?? doc.created_at ?? null
}

/** shortHash keeps the algorithm and the first few hex digits -- enough to check
 *  by eye against a consent record, short enough for a table cell. */
export function shortHash(hash: string, keep = 4): string {
  const [algo, digest] = hash.includes(':') ? hash.split(':', 2) : ['sha256', hash]
  if (!digest) return hash
  return `${algo}:${digest.slice(0, keep)}…`
}

export function ConsentDocuments() {
  const me = useMe()
  const docs = useConsentDocuments()
  const mayManage = can(me.data, 'consent.manage')

  // Kept in the URL so "Tạo v3" on a form's compliance tab can link straight
  // into the composer, and so a reload does not throw away the draft's context.
  const [params, setParams] = useSearchParams()
  const composing = params.get('new') === '1'

  function setComposing(on: boolean) {
    const next = new URLSearchParams(params)
    if (on) next.set('new', '1')
    else next.delete('new')
    setParams(next, { replace: true })
  }

  const rows = [...(docs.data ?? [])].sort(
    (a, b) => a.kind.localeCompare(b.kind) || b.version_no - a.version_no,
  )

  return (
    <div className="flex flex-col gap-3">
      {mayManage && !composing && (
        <div className="flex justify-end">
          <button type="button" className="btn-primary" onClick={() => setComposing(true)}>
            + Version mới
          </button>
        </div>
      )}
      {!mayManage && (
        // The button is not rendered at all; this says why, so the absence reads
        // as a permission boundary rather than as a missing feature.
        <p className="id-chip text-right">
          Chỉ vai trò có <span className="font-semibold">consent.manage</span> tạo được version mới.
        </p>
      )}

      {composing && mayManage && <Composer onDone={() => setComposing(false)} />}

      {docs.isPending && <Loading />}
      {docs.isError && <ErrorBanner error={docs.error} retry={() => void docs.refetch()} />}

      {docs.data && rows.length === 0 && (
        <Empty
          title="Chưa có văn bản đồng ý nào"
          hint="Biểu mẫu thu thập dữ liệu cá nhân phải gắn với một văn bản đã publish. Tạo version đầu tiên trước khi cho biểu mẫu chạy."
        />
      )}

      {rows.length > 0 && (
        <Table
          head={
            <>
              <Th>Loại</Th>
              <Th>Version</Th>
              <Th>Publish</Th>
              <Th>sha256</Th>
              <Th>
                <span className="sr-only">Hành động</span>
              </Th>
            </>
          }
        >
          {rows.map((doc) => {
            const active = activeDocument(docs.data, doc.kind as DocumentKind)?.id === doc.id
            return (
              <Tr key={doc.id}>
                <Td>
                  <div className="font-semibold">{documentKindLabel(doc.kind)}</div>
                  <div className="id-chip">{doc.id}</div>
                </Td>
                <Td className="font-mono text-meta">
                  v{doc.version_no}
                  {active && <span className="ml-1 font-sans text-meta text-accent">đang dùng</span>}
                </Td>
                <Td className="whitespace-nowrap">{date(publishedAt(doc))}</Td>
                <Td>
                  <div className="id-chip">{shortHash(doc.content_hash)}</div>
                  <div className="mt-0.5">
                    <StatusPill>bất biến</StatusPill>
                  </div>
                </Td>
                <Td className="text-right">
                  {/* The permalink is the immutable copy the consent record points
                      at, so it is what a person checking the record must read. */}
                  <a
                    className="btn inline-block"
                    href={doc.permalink ?? `/p/${doc.id}`}
                    target="_blank"
                    rel="noreferrer"
                  >
                    Xem
                  </a>
                </Td>
              </Tr>
            )
          })}
        </Table>
      )}

      <p className="id-chip">
        Đã publish là bất biến: sửa nội dung nghĩa là publish một version mới. Bản ghi đồng ý cũ vẫn
        trỏ về đúng văn bản mà chủ thể đã đọc.
      </p>
    </div>
  )
}

/** Composer publishes the next version of a document. */
function Composer({ onDone }: { onDone: () => void }) {
  const qc = useQueryClient()
  const [kind, setKind] = useState<DocumentKind>('consent_text')
  const [bodyHtml, setBodyHtml] = useState('')
  const [created, setCreated] = useState<CreatedDocument | null>(null)

  const publish = useMutation({
    mutationFn: (body: { kind: DocumentKind; body_html: string }) =>
      api.post<CreatedDocument>('/api/v1/consent/documents', body),
    onSuccess: async (doc) => {
      setCreated(doc)
      setBodyHtml('')
      await qc.invalidateQueries({ queryKey: ['consent-documents'] })
    },
  })

  const fields = publish.error instanceof RequestFailed ? publish.error.fields : {}

  if (created) {
    return (
      <Card title="Đã publish version mới" aside={`v${created.version_no}`}>
        <p className="text-body">
          Version này không sửa được nữa. Biểu mẫu đang chạy sẽ dùng nó cho lượt gửi tiếp theo.
        </p>
        <p className="id-chip mt-1">
          {created.id} · {shortHash(created.content_hash, 8)}
        </p>
        <div className="mt-3 flex gap-2">
          <a className="btn" href={created.permalink} target="_blank" rel="noreferrer">
            Xem bản đã publish
          </a>
          <button type="button" className="btn" onClick={onDone}>
            Xong
          </button>
        </div>
      </Card>
    )
  }

  return (
    <Card title="Publish version mới" aside="không sửa được sau khi publish">
      <form
        className="flex flex-col gap-3"
        onSubmit={(e) => {
          e.preventDefault()
          publish.mutate({ kind, body_html: bodyHtml })
        }}
      >
        <Field label="Loại văn bản" error={fields.kind}>
          <select
            id="doc-kind"
            className="input"
            value={kind}
            onChange={(e) => setKind(e.target.value as DocumentKind)}
          >
            <option value="consent_text">Văn bản đồng ý</option>
            <option value="privacy_notice">Thông báo xử lý DLCN</option>
          </select>
        </Field>

        <Field
          label="Nội dung (HTML)"
          hint="Chính xác nội dung chủ thể dữ liệu sẽ đọc. Client băm đúng phần đã hiển thị và server đối chiếu lại khi nhận lượt gửi."
          error={fields.body_html}
        >
          <textarea
            id="doc-body"
            className="input min-h-40 font-mono text-body"
            value={bodyHtml}
            onChange={(e) => setBodyHtml(e.target.value)}
            required
          />
        </Field>

        {publish.isError && !Object.keys(fields).length && <ErrorBanner error={publish.error} />}

        <div className="flex gap-2">
          <button type="submit" className="btn-primary" disabled={publish.isPending || !bodyHtml}>
            {publish.isPending ? 'Đang publish…' : 'Publish'}
          </button>
          <button type="button" className="btn" onClick={onDone}>
            Huỷ
          </button>
        </div>
      </form>
    </Card>
  )
}
