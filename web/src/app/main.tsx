import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { BrowserRouter, Navigate, Route, Routes } from 'react-router'
import '../index.css'
import { Shell } from './components/Shell'
import { mfaGated, useMe } from './lib/session'

import { Login } from './routes/Login'
import { Setup } from './routes/Setup'
import { Forms } from './routes/Forms'
import { Builder } from './routes/builder/Builder'
import { Submissions } from './routes/submissions/Submissions'
import { Links } from './routes/links/Links'
import { LinkDetail } from './routes/links/LinkDetail'
import { CreateLink } from './routes/links/CreateLink'
import { Domains } from './routes/links/Domains'
import { RateLimitedDemo } from './routes/links/RateLimited'
import { Funnel } from './routes/analytics/Funnel'
import { AuditLog } from './routes/audit/AuditLog'
import { ComplianceCentre } from './routes/compliance/ComplianceCentre'
import { FormCompliance } from './routes/compliance/FormCompliance'
import { DSRQueue } from './routes/dsr/DSRQueue'
import { DSRRequest } from './routes/dsr/DSRRequest'
import { LegalHold } from './routes/dsr/LegalHold'
import { Members } from './routes/members/Members'
import { Invitations } from './routes/members/Invitations'
import { RoleMatrix } from './routes/members/RoleMatrix'
import { Webhooks } from './routes/integrations/Webhooks'
import { WebhookDeliveries } from './routes/integrations/WebhookDeliveries'
import { ApiKeys } from './routes/integrations/ApiKeys'
import { Files } from './routes/integrations/Files'
import { Account } from './routes/account/Account'
import { EnableMFA } from './routes/account/EnableMFA'
import { ForgotPassword } from './routes/account/ForgotPassword'
import { ResetPassword } from './routes/account/ResetPassword'
import { AcceptInvite } from './routes/account/AcceptInvite'

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      // The admin screens show compliance deadlines. Refetching on focus means
      // an operator who leaves a tab open over lunch does not act on an SLA
      // countdown that expired while they were away.
      refetchOnWindowFocus: true,
      retry: (count, err) => count < 2 && !(err as { status?: number }).status,
    },
  },
})

function Protected() {
  const me = useMe()
  if (me.isPending) return <div className="p-6 text-body text-muted">Đang tải…</div>
  if (!me.data) return <Navigate to="/login" replace />
  // One gate, not a permission error on every screen. The account is signed in
  // and its role is real; it simply holds nothing until a second factor exists.
  if (mfaGated(me.data)) return <Navigate to="/account/mfa" replace />
  return <Shell />
}

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <BrowserRouter>
        <Routes>
          {/* Signed out, or on the way in. */}
          <Route path="/login" element={<Login />} />
          <Route path="/setup" element={<Setup />} />
          <Route path="/password/forgot" element={<ForgotPassword />} />
          <Route path="/reset-password" element={<ResetPassword />} />
          <Route path="/invite" element={<AcceptInvite />} />

          {/* Enrolling a second factor revokes every session, including the one
              doing the enrolling. Inside Protected, the next useMe refetch would
              redirect to /login and take the ten recovery codes on screen with
              it -- and they are shown exactly once. */}
          <Route path="/account/mfa" element={<EnableMFA />} />

          <Route element={<Protected />}>
            <Route
              path="/"
              element={<div className="p-6 text-[13px]">Chọn một dự án ở cột trái.</div>}
            />
            <Route path="/account" element={<Account />} />

            {/* Inside a project. */}
            <Route path="/p/:projectId" element={<Navigate to="forms" replace />} />
            <Route path="/p/:projectId/forms" element={<Forms />} />
            <Route path="/p/:projectId/forms/:formId/builder" element={<Builder />} />
            <Route path="/p/:projectId/forms/:formId/compliance" element={<FormCompliance />} />
            <Route path="/p/:projectId/forms/:formId/attachments" element={<Files />} />
            <Route path="/p/:projectId/forms/:formId/submissions" element={<Submissions />} />
            <Route path="/p/:projectId/submissions" element={<Submissions />} />

            {/* Static segments outrank the :linkId wildcard in react-router's
                own ranking, so declaration order here is not load-bearing. */}
            <Route path="/p/:projectId/links" element={<Links />} />
            <Route path="/p/:projectId/links/new" element={<CreateLink />} />
            <Route path="/p/:projectId/links/domains" element={<Domains />} />
            <Route path="/p/:projectId/links/:linkId" element={<LinkDetail />} />
            <Route path="/p/:projectId/links/:linkId/legal-hold" element={<LegalHold />} />

            <Route path="/p/:projectId/analytics" element={<Funnel />} />
            <Route path="/p/:projectId/analytics/:formId" element={<Funnel />} />

            <Route path="/p/:projectId/integrations/webhooks" element={<Webhooks />} />
            <Route
              path="/p/:projectId/integrations/webhooks/:webhookId/deliveries"
              element={<WebhookDeliveries />}
            />

            {/* Organisation-wide. */}
            <Route path="/compliance" element={<ComplianceCentre />} />
            <Route path="/compliance/dsr" element={<DSRQueue />} />
            <Route path="/compliance/dsr/:requestId" element={<DSRRequest />} />
            <Route path="/audit" element={<AuditLog />} />
            <Route path="/members" element={<Members />} />
            <Route path="/members/invitations" element={<Invitations />} />
            <Route path="/members/roles" element={<RoleMatrix />} />
            <Route path="/api-keys" element={<ApiKeys />} />
            <Route path="/rate-limits" element={<RateLimitedDemo />} />
          </Route>

          {/* An unknown admin path is a mistyped URL, not a signed-out user. */}
          <Route
            path="*"
            element={
              <div className="p-6 text-[13px]">
                Không có trang này. <a href="/">Về trang chủ</a>
              </div>
            }
          />
        </Routes>
      </BrowserRouter>
    </QueryClientProvider>
  </StrictMode>,
)
